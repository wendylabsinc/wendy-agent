package meshcatalog

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

const (
	SyncPort = 43022
	syncALPN = "wendy-mesh-catalog/1"
)

// Runtime synchronizes signed catalog records over authenticated routed mesh
// sessions. Its only dependency on the mesh node is a read-only snapshot
// callback, avoiding a localmesh -> meshcatalog import cycle. Records relay
// hop by hop, so a device reachable only through neighbours still converges.
// Run until the context is cancelled.
type Runtime struct {
	catalog                *Catalog
	snapshot               func() localmesh.NodeSnapshot
	mu                     sync.Mutex
	peers                  map[meshid.Device]*catalogPeer
	tickets                *localmesh.TicketStore
	dialing                map[meshid.Device]bool
	closed                 bool
	onGatewayChange        func()
	inventoryRetries       map[meshid.Device]inventoryRetry
	inventoryOverflowUntil time.Time
	// Tests can route the production outbound path through a real loopback listener.
	dialPeer func(context.Context, netip.Addr, netip.Addr) (net.Conn, error)
}

type catalogPeer struct {
	conn net.Conn
	out  chan SignedRecord
	// ready means an initial, ordered snapshot from this authenticated peer
	// has been received and all missing origin identities were repaired.
	ready bool
}

func NewRuntime(catalog *Catalog, snapshot func() localmesh.NodeSnapshot) (*Runtime, error) {
	if catalog == nil || snapshot == nil {
		return nil, errors.New("missing mesh catalog runtime dependency")
	}
	return &Runtime{catalog: catalog, snapshot: snapshot, peers: map[meshid.Device]*catalogPeer{},
		dialing: map[meshid.Device]bool{},
		tickets: catalog.creds.ServerTicketStore("catalog", "catalog-tls")}, nil
}

// Broadcast schedules an admitted local or relayed record for all live peers.
// Periodic full reconciliation repairs a full queue and any partition.
func (r *Runtime) Broadcast(w SignedRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, peer := range r.peers {
		// Send expressions are evaluated even when select chooses default.
		// Avoid copying a record that a full queue will drop. Broadcast is
		// the only producer and holds mu; consumers can only free capacity.
		if len(peer.out) == cap(peer.out) {
			continue
		}
		select {
		case peer.out <- cloneWire(w):
		default:
		}
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("missing mesh catalog context")
	}
	self := r.catalog.device.MeshAddress()
	listenAddr := net.JoinHostPort(self.String(), fmt.Sprint(SyncPort))
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var listener net.Listener
	var err error
	defer func() {
		if listener != nil {
			_ = listener.Close()
		}
		r.mu.Lock()
		r.closed = true
		for _, peer := range r.peers {
			_ = peer.conn.Close()
		}
		r.mu.Unlock()
	}()
	for {
		if listener == nil {
			listener, err = net.Listen("tcp", listenAddr)
			if err == nil {
				go r.accept(ctx, listener)
			}
		}
		r.discover(ctx, time.Now())
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (r *Runtime) accept(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go r.inbound(ctx, conn)
	}
}

func (r *Runtime) serverTLS() *tls.Config {
	config := &tls.Config{
		Certificates: []tls.Certificate{r.catalog.creds.Certificate},
		MinVersion:   tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAnyClientCert,
		NextProtos: []string{syncALPN},
		VerifyConnection: func(state tls.ConnectionState) error {
			chain := make([][]byte, 0, len(state.PeerCertificates))
			for _, cert := range state.PeerCertificates {
				chain = append(chain, cert.Raw)
			}
			id, err := r.catalog.creds.Verify(chain, time.Now())
			if err != nil {
				return err
			}
			if meshid.Compare(id.Device, r.catalog.device) >= 0 {
				return errors.New("unacceptable mesh catalog peer")
			}
			return nil
		},
	}
	r.tickets.Configure(config)
	return config
}

func (r *Runtime) inbound(ctx context.Context, raw net.Conn) {
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
	conn := tls.Server(raw, r.serverTLS())
	if err := conn.HandshakeContext(ctx); err != nil {
		return
	}
	state := conn.ConnectionState()
	if !catalogALPN(state.NegotiatedProtocol) || len(state.PeerCertificates) == 0 {
		return
	}
	chain := make([][]byte, 0, len(state.PeerCertificates))
	for _, cert := range state.PeerCertificates {
		chain = append(chain, cert.Raw)
	}
	id, err := r.catalog.creds.Verify(chain, time.Now())
	if err != nil || meshid.Compare(id.Device, r.catalog.device) >= 0 {
		return
	}
	remote, err := netip.ParseAddrPort(raw.RemoteAddr().String())
	if err != nil {
		return
	}
	if remote.Addr().Unmap() != id.Device.MeshAddress() || !r.eligible(id.Device, time.Now()) {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	r.sessionInventory(ctx, id.Device, conn)
}

func (r *Runtime) discover(ctx context.Context, now time.Time) {
	view := r.snapshot()
	// Synchronize with reachable origins, including origins multiple hops away.
	// Projection readiness belongs to the origin's fresh inventory session.
	for _, manifest := range view.Devices {
		device := meshid.NewDevice(r.catalog.device.Tenant(), manifest.Device)
		if meshid.Compare(device, r.catalog.device) <= 0 || !eligibleSnapshot(view, device, now) {
			continue
		}
		if _, allowed := r.inventoryAttempt(device, now); !allowed {
			continue
		}
		r.mu.Lock()
		_, connected := r.peers[device]
		if !connected && !r.dialing[device] && !r.closed {
			r.dialing[device] = true
			go r.outbound(ctx, device)
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	for device, peer := range r.peers {
		if !eligibleSnapshot(view, device, now) {
			_ = peer.conn.Close()
		}
	}
	r.mu.Unlock()
}

func eligibleSnapshot(view localmesh.NodeSnapshot, device meshid.Device, now time.Time) bool {
	manifestOK := false
	for _, m := range view.Devices {
		if m.Device == device.UUID() && !m.Withdraw && time.UnixMilli(m.Expires).After(now) {
			manifestOK = true
			break
		}
	}
	if !manifestOK {
		return false
	}
	prefix := netip.PrefixFrom(device.MeshAddress(), 128)
	for _, route := range view.Routes {
		if route.Prefix == prefix && !route.Local && !route.Unreachable {
			return true
		}
	}
	return false
}

func (r *Runtime) eligible(device meshid.Device, now time.Time) bool {
	return eligibleSnapshot(r.snapshot(), device, now)
}

func catalogALPN(protocol string) bool { return protocol == syncALPN }

// ProjectionReady is true only after a current route and a fresh, complete
// anti-entropy snapshot from the service origin. Cached records alone can be
// stale when the origin withdrew a service during a partition.
func (r *Runtime) ProjectionReady(device meshid.Device) bool {
	if !r.eligible(device, time.Now()) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	peer := r.peers[device]
	return peer != nil && peer.ready
}

// InvalidateProjection forces a new snapshot after the origin's route was
// observed down. Closing the old session also handles a brief cut shorter than
// Runtime's periodic route scan, for which a previously completed snapshot
// would otherwise still look current on heal.
func (r *Runtime) InvalidateProjection(device meshid.Device) {
	r.mu.Lock()
	peer := r.peers[device]
	if peer != nil {
		peer.ready = false
	}
	r.mu.Unlock()
	if peer != nil {
		_ = peer.conn.Close()
	}
}

func (r *Runtime) outbound(ctx context.Context, device meshid.Device) {
	defer func() { r.mu.Lock(); delete(r.dialing, device); r.mu.Unlock() }()
	cold, allowed := r.reserveInventoryAttempt(device, time.Now())
	if !allowed {
		return
	}
	handedOff := false
	defer func() {
		if cold && !handedOff {
			r.inventoryFailed(device, true, time.Now())
		}
	}()
	self := r.catalog.device.MeshAddress()
	peer := device.MeshAddress()
	conf, err := r.catalog.creds.PeerTLSWithTickets(device, syncALPN, "catalog-tls")
	if err != nil {
		return
	}
	// This unshipped protocol has one end state, including cache inventory.
	conf.NextProtos = []string{syncALPN}
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dial := r.dialPeer
	if dial == nil {
		dial = func(ctx context.Context, self, peer netip.Addr) (net.Conn, error) {
			dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(self.AsSlice())}}
			return dialer.DialContext(ctx, "tcp6", net.JoinHostPort(peer.String(), fmt.Sprint(SyncPort)))
		}
	}
	raw, err := dial(dialCtx, self, peer)
	if err != nil {
		return
	}
	defer raw.Close()
	conn := tls.Client(raw, conf)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		return
	}
	if conn.ConnectionState().NegotiatedProtocol != syncALPN || !r.eligible(device, time.Now()) {
		return
	}
	handedOff = true
	r.sessionInventoryReserved(ctx, device, conn, cold)
}
