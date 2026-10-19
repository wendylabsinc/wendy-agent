//go:build linux

package nanprovider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/vishvananda/netlink"
	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// LinkNode is the common Babel node shared with configured TCP and other
// carriers. AttachWithCost verifies the wendy net hello after QUIC mutual TLS.
type LinkNode interface {
	AttachWithCost(context.Context, meshid.Device, *quic.Conn, uint16) error
}

var _ LinkNode = (*localmesh.Node)(nil)

// NAN costs twice a configured TCP link. BLE will use a substantially larger
// metric so a healthy NAN path remains preferred.
const NANLinkCost uint16 = 512

// Provider owns one Wi-Fi Aware session and its NDI. The caller owns and runs
// the shared localmesh Node. Recreate this provider when enrolled identity or
// trust material changes. Run returns on radio failure so the caller can retry
// after a bounded backoff; it drains owned NDPs before removing the NDI.
type Provider struct {
	Credentials *localmesh.Credentials
	Node        LinkNode
	Selection   *localmesh.PeerSelection
	Logger      *zap.Logger
	// Status receives local setup readiness/failure, not peer reachability.
	// The optional callback must return promptly.
	Status func(localmesh.CarrierStatus)
}

func (p Provider) Run(parent context.Context) error {
	if p.Credentials == nil || p.Node == nil {
		return errors.New("NAN provider needs credentials and a wendy net node")
	}
	id := p.Credentials.Device
	logger := p.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	release, err := reserveNDI(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = reclaimOwnedNDI(ctx, id)
	if err != nil {
		return err
	}
	status, err := helperOutput(ctx, "status")
	if err != nil && !absentNANStatus(status, err) {
		return err
	}
	// A stopped nan0 still belongs to its existing creator. Recovering our
	// private NDI also does not establish ownership of that shared interface.
	present := err == nil
	var currentSocket socketIdentity
	if present {
		currentSocket, err = nanControlSocketIdentity()
		if err != nil {
			return fmt.Errorf("existing NAN control socket missing: %w", err)
		}
	}
	marked, err := sessionOwnerMatches(id, currentSocket)
	if err != nil {
		return err
	}
	plan := planSession(present, marked)
	if hasAppNANNDI() {
		// App-entitled sessions share this radio-wide cluster. If we inherited
		// an ownership marker from an older provider, relinquish it instead
		// of resetting nan0 out from under the app.
		plan = sessionPlan{}
		if marked {
			if err := clearSessionOwner(id); err != nil {
				return err
			}
		}
	}
	if plan.own {
		// Record ownership before START: a process killed during START must not
		// leave an unclaimable nan0. The exclusive provider lock is held here.
		if err := markSessionOwner(id, socketIdentity{}); err != nil {
			return err
		}
		defer func() {
			// A host-shell stop/start can replace nan0 while this provider is
			// running. Never stop an interface whose socket is no longer ours.
			current, socketErr := nanControlSocketIdentity()
			if errors.Is(socketErr, os.ErrNotExist) {
				if err := clearSessionOwner(id); err != nil {
					logger.Warn("NAN ownership marker removal failed", zap.Error(err))
				}
				return
			}
			if socketErr != nil {
				logger.Warn("NAN ownership check failed; preserving interface", zap.Error(socketErr))
				return
			}
			owned, ownerErr := sessionOwnerMatches(id, current)
			if ownerErr != nil || !owned {
				logger.Warn("NAN ownership changed; preserving interface", zap.Error(ownerErr))
				return
			}
			if hasAppNANNDI() {
				logger.Info("Preserving NAN interface for app-owned NDP sessions")
				if err := clearSessionOwner(id); err != nil {
					logger.Warn("NAN ownership marker removal failed", zap.Error(err))
				}
				return
			}
			if err := runCleanupHelper("stop"); err != nil {
				logger.Warn("NAN stop failed", zap.Error(err))
			} else if err := clearSessionOwner(id); err != nil {
				logger.Warn("NAN ownership marker removal failed", zap.Error(err))
			}
		}()
	}
	if plan.reset {
		// An interrupted provider can leave old service handles and a live
		// cluster behind. Clear them before opening this provider's radio.
		if hasAppNANNDI() {
			return errors.New("cannot reset shared NAN interface while an app NDI exists")
		}
		if err := runHelper(ctx, "stop"); err != nil {
			return fmt.Errorf("reset abandoned NAN session: %w", err)
		}
	}
	if err := runHelper(ctx, "start"); err != nil {
		return err
	}
	if plan.own {
		currentSocket, err := nanControlSocketIdentity()
		if err != nil {
			return fmt.Errorf("started NAN control socket missing: %w", err)
		}
		if err := markSessionOwner(id, currentSocket); err != nil {
			return err
		}
	}
	if err := runHelper(ctx, "schedule-default"); err != nil {
		return err
	}
	// A false-connected NDP first gets an NDI-only reset. If a second epoch
	// still has no RX, an agent-owned nan0 gets one stop/start. The helper's
	// stop preserves the supplicant and station Wi-Fi; P2P restoration is an
	// explicit, separate operation. External nan0 is never restarted here.
	recovery := nanRecovery{ownsSession: plan.own}
	for {
		err := p.runNDIEpoch(ctx, id, logger, recovery.canReset())
		if err != nil && ctx.Err() == nil && p.Status != nil {
			p.Status(localmesh.CarrierStatus{Err: err})
		}
		if errors.Is(err, errSoftNANRecovery) {
			switch recovery.next(errors.Is(err, errNoRXNANRecovery)) {
			case resetNDI:
				logger.Warn("NAN data path unavailable; recreating only agent-owned NDI and service handles")
				continue
			case resetOwnedSession:
				logger.Warn("NAN data path unavailable after NDI reset; restarting agent-owned NAN interface")
				if err = restartOwnedNAN(ctx, id); err == nil {
					continue
				}
				logger.Error("NAN interface recovery failed; preserving supplicant and Wi-Fi until carrier configuration changes", zap.Error(err))
			}
		}
		if recovery.attempted() && err != nil && ctx.Err() == nil {
			// A failed recovery stays in place until the carrier config changes.
			// The provider's deferred cleanup will stop only its own nan0.
			logger.Error("NAN soft recovery failed; keeping supplicant and Wi-Fi up until carrier configuration changes", zap.Error(err))
			<-ctx.Done()
			return ctx.Err()
		}
		return err
	}
}

type nanResetAction uint8

const (
	resetNone nanResetAction = iota
	resetNDI
	resetOwnedSession
)

type nanRecovery struct {
	ownsSession, ndiReset, sessionReset bool
}

func (r nanRecovery) canReset() bool {
	return !r.ndiReset || (r.ownsSession && !r.sessionReset)
}

func (r nanRecovery) attempted() bool { return r.ndiReset || r.sessionReset }

func (r *nanRecovery) next(noRX bool) nanResetAction {
	if !r.ndiReset {
		r.ndiReset = true
		return resetNDI
	}
	if r.ownsSession && noRX && !r.sessionReset {
		r.sessionReset = true
		return resetOwnedSession
	}
	return resetNone
}

func restartOwnedNAN(ctx context.Context, id meshid.Device) error {
	if hasAppNANNDI() {
		return errors.New("cannot restart shared NAN interface while an app NDI exists")
	}
	current, err := nanControlSocketIdentity()
	if err != nil {
		return fmt.Errorf("NAN control socket before owned restart: %w", err)
	}
	owned, err := sessionOwnerMatches(id, current)
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("NAN ownership changed before restart")
	}
	if err := runHelper(ctx, "stop"); err != nil {
		return fmt.Errorf("stop owned NAN interface: %w", err)
	}
	if err := markSessionOwner(id, socketIdentity{}); err != nil {
		return err
	}
	if err := runHelper(ctx, "start"); err != nil {
		return fmt.Errorf("restart owned NAN interface: %w", err)
	}
	current, err = nanControlSocketIdentity()
	if err != nil {
		return fmt.Errorf("NAN control socket after owned restart: %w", err)
	}
	if err := markSessionOwner(id, current); err != nil {
		return err
	}
	return runHelper(ctx, "schedule-default")
}

var appNANNDIPattern = regexp.MustCompile(`^wa[0-9a-f]{12}$`)

func hasAppNANNDI() bool {
	ifaces, err := net.Interfaces()
	if err != nil {
		// Preserve shared nan0 when we cannot establish that it is unused.
		return true
	}
	for _, iface := range ifaces {
		if appNANNDIPattern.MatchString(iface.Name) {
			return true
		}
	}
	return false
}

func (p Provider) runNDIEpoch(parent context.Context, id meshid.Device, logger *zap.Logger, allowSoftReset bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// The exclusive reservation and initial reclaim have established that this
	// private interface is unused. A helper can create it and then fail (or be
	// canceled) before returning, so arm cleanup before invoking interface_add.
	defer func() {
		if err := runCleanupHelper("ndi-remove", ndiName); err != nil {
			logger.Warn("NAN NDI removal failed", zap.Error(err))
		}
	}()
	if err := runHelper(ctx, "ndi-create", ndiName); err != nil {
		return err
	}
	ndi, err := netlink.LinkByName(ndiName)
	if err != nil {
		return err
	}
	self, err := underlayAddr(id)
	if err != nil {
		return err
	}
	addr, _ := netlink.ParseAddr(self.String() + "/16")
	if err = netlink.AddrReplace(ndi, addr); err != nil {
		return err
	}
	if err = netlink.LinkSetUp(ndi); err != nil {
		return err
	}
	peers := make(chan radioPeer, 8)
	health := make(chan radioHealth, 32)
	radioDone := make(chan error, 1)
	go func() { radioDone <- runRadio(ctx, id, peers, health, p.Selection, allowSoftReset, logger, p.Status) }()
	// runRadio drains NDPs before returning. Do not remove the NDI until then.
	defer func() { cancel(); <-radioDone }()
	type worker struct {
		peer   radioPeer
		cancel context.CancelFunc
		done   chan struct{}
	}
	running := map[meshid.Device]worker{}
	var workers sync.WaitGroup
	defer func() {
		cancel()
		for _, w := range running {
			w.cancel()
		}
		workers.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-radioDone:
			// The deferred cleanup still awaits the radio channel. Put the result
			// back, so a failing radio cannot deadlock provider shutdown.
			radioDone <- err
			return err
		case peer := <-peers:
			if previous, ok := running[peer.Device]; ok {
				if previous.peer == peer {
					continue
				}
				// Ignore teardown for a superseded NDP incarnation.
				if peer.NDI == "" && peer.ID != "" && previous.peer.ID != peer.ID {
					continue
				}
				previous.cancel()
				<-previous.done
				delete(running, peer.Device)
			}
			if peer.NDI == "" || len(running) >= 3 || (p.Selection != nil && !p.Selection.AllowRadio(peer.Device, localmesh.RadioNAN)) {
				continue
			}
			peerCtx, peerCancel := context.WithCancel(ctx)
			done := make(chan struct{})
			running[peer.Device] = worker{peer, peerCancel, done}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer close(done)
				defer peerCancel()
				for peerCtx.Err() == nil {
					err := p.attachPeer(peerCtx, id, peer, ndi, func(up bool) {
						select {
						case health <- radioHealth{Peer: peer, Up: up}:
						case <-peerCtx.Done():
						}
					})
					if err != nil && peerCtx.Err() == nil {
						logger.Warn("NAN peer session failed", zap.String("device", peer.Device.UUID().String()), zap.Error(err))
					}
					select {
					case <-peerCtx.Done():
						return
					case <-time.After(3 * time.Second):
					}
				}
			}()
		}
	}
}

func absentNANStatus(output string, err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1 && strings.TrimSpace(output) == "nan0 is not present"
}

// The process can be killed after creating its NDI but before its deferred
// cleanup runs. The provider lock is held here, and its private underlay
// address distinguishes that abandoned NDI from an interface made by a host
// shell user. Never remove an interface with another address.
func reclaimOwnedNDI(ctx context.Context, id meshid.Device) (bool, error) {
	link, err := netlink.LinkByName(ndiName)
	if _, absent := err.(netlink.LinkNotFoundError); absent {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return false, err
	}
	if !ownedNDIAddress(addrs, id) {
		return false, fmt.Errorf("NAN interface %s is already in use", ndiName)
	}
	if err := runHelper(ctx, "ndi-remove", ndiName); err != nil {
		return false, fmt.Errorf("remove abandoned NAN interface %s: %w", ndiName, err)
	}
	if _, err := netlink.LinkByName(ndiName); err == nil {
		return false, fmt.Errorf("abandoned NAN interface %s still exists after removal", ndiName)
	} else if _, absent := err.(netlink.LinkNotFoundError); !absent {
		return false, err
	}
	return true, nil
}

func ownedNDIAddress(addrs []netlink.Addr, id meshid.Device) bool {
	expected, err := underlayAddr(id)
	if err != nil {
		return false
	}
	return len(addrs) == 1 && addrs[0].IPNet != nil && addrs[0].IPNet.String() == expected.String()+"/16"
}

// underlayAddr derives the NAN underlay address from the last two bytes of
// the device UUID. The NAN single-NDI model needs distinct addresses for this
// host and its directly attached peers inside 10.89.0.0/16. Network/broadcast
// addresses stay reserved, and peer admission refuses colliding UUID suffixes.
func underlayAddr(id meshid.Device) (netip.Addr, error) {
	u := id.UUID()
	addr := netip.AddrFrom4([4]byte{10, 89, u[14], u[15]})
	if addr == netip.AddrFrom4([4]byte{10, 89, 0, 0}) || addr == netip.AddrFrom4([4]byte{10, 89, 255, 255}) {
		return netip.Addr{}, fmt.Errorf("device %s derives reserved NAN underlay address %s", id.UUID(), addr)
	}
	return addr, nil
}

func boundSocket(iface string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var inner error
		err := raw.Control(func(fd uintptr) {
			inner = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
		})
		if err != nil {
			return err
		}
		return inner
	}
}

func (p Provider) attachPeer(ctx context.Context, id meshid.Device, peer radioPeer, ndi netlink.Link, connected func(bool)) error {
	mac, err := net.ParseMAC(peer.NDI)
	if err != nil {
		return err
	}
	self, err := underlayAddr(id)
	if err != nil {
		return err
	}
	remote, err := underlayAddr(peer.Device)
	if err != nil {
		return err
	}
	neighbor := &netlink.Neigh{LinkIndex: ndi.Attrs().Index, IP: net.IP(remote.AsSlice()), HardwareAddr: mac, State: netlink.NUD_PERMANENT}
	if err = netlink.NeighSet(neighbor); err != nil {
		return err
	}
	defer func() { _ = netlink.NeighDel(neighbor) }()
	// Presence SSI only proposes a device UUID. The enrolled certificate pins
	// the full device principal during this QUIC handshake.
	tlsConfig, err := p.Credentials.PeerTLSWithTickets(peer.Device, localmesh.LinkALPN, localmesh.LinkQUICSessionScope)
	if err != nil {
		return err
	}
	initiator := meshid.Compare(id, peer.Device) < 0
	lower := id.UUID()
	if !initiator {
		lower = peer.Device.UUID()
	}
	port := 20000 + (int(lower[14])<<8|int(lower[15]))%40000
	localPort := 0
	if !initiator {
		localPort = port
	}
	lc := net.ListenConfig{Control: boundSocket(ndiName)}
	udp, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort(self.String(), strconv.Itoa(localPort)))
	if err != nil {
		return err
	}
	defer udp.Close()
	transport := &quic.Transport{Conn: udp}
	defer transport.Close()
	var conn *quic.Conn
	if initiator {
		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		conn, err = transport.Dial(dialCtx, &net.UDPAddr{IP: net.IP(remote.AsSlice()), Port: port}, tlsConfig, localmesh.QUICConfig())
	} else {
		listener, listenErr := transport.Listen(tlsConfig, localmesh.QUICConfig())
		if listenErr != nil {
			return listenErr
		}
		defer listener.Close()
		acceptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		conn, err = listener.Accept(acceptCtx)
		cancel()
	}
	if err != nil {
		return fmt.Errorf("NAN QUIC peer %s: %w", peer.Device.UUID(), err)
	}
	defer conn.CloseWithError(0, "NAN path stopped")
	connected(true)
	defer connected(false)
	return p.Node.AttachWithCost(ctx, peer.Device, conn, NANLinkCost)
}
