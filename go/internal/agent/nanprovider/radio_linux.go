//go:build linux

package nanprovider

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

const ndiName = "wnanndi0"

type radioControl struct {
	conn *net.UnixConn
	dir  string
	mu   sync.Mutex
}

func openRadio() (*radioControl, error) {
	dir, err := os.MkdirTemp("/run", "wendy-nan-ctrl-")
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUnix("unixgram", &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unixgram"}, &net.UnixAddr{Name: "/run/wpa_supplicant/nan0", Net: "unixgram"})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &radioControl{conn: conn, dir: dir}, nil
}
func (c *radioControl) close() { _ = c.conn.Close(); _ = os.RemoveAll(c.dir) }
func (c *radioControl) command(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.conn.Write([]byte(cmd)); err != nil {
		return "", err
	}
	buf := make([]byte, 8192)
	n, _, flags, _, err := c.conn.ReadMsgUnix(buf, nil)
	if err != nil {
		return "", err
	}
	if flags&unix.MSG_TRUNC != 0 {
		return "", fmt.Errorf("supplicant reply exceeds %d bytes", len(buf))
	}
	reply := strings.TrimSpace(string(buf[:n]))
	if strings.HasPrefix(reply, "FAIL") || reply == "UNKNOWN COMMAND" {
		return reply, fmt.Errorf("supplicant %s: %s", strings.Fields(cmd)[0], reply)
	}
	return reply, nil
}
func runHelper(ctx context.Context, args ...string) error {
	_, err := helperOutput(ctx, args...)
	return err
}

const nanHelperTimeout = 20 * time.Second

func helperOutput(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, nanHelperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/sbin/wendyos-nan", args...)
	cmd.Env = append(os.Environ(), "WENDYOS_NAN_NDI_IFACE="+ndiName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("wendyos-nan %v: %w: %s", args, err, out)
	}
	return string(out), nil
}

type radioPeer struct {
	Device meshid.Device
	NMI    string
	NDI    string
	ID     string
}

func validNDPID(raw string) bool {
	id, err := strconv.Atoi(raw)
	return err == nil && id >= 1 && id <= 255
}

func validHandle(raw string) bool {
	id, err := strconv.ParseUint(raw, 10, 32)
	return err == nil && id > 0
}

const (
	radioAttemptCooldown = 15 * time.Second
	maxRadioAttempts     = 128
)

func expireRadioAttempts(attempts map[string]time.Time, now time.Time) {
	for nmi, last := range attempts {
		if now.Sub(last) >= radioAttemptCooldown {
			delete(attempts, nmi)
		}
	}
}

func rememberRadioAttempt(attempts map[string]time.Time, nmi string, now time.Time) {
	expireRadioAttempts(attempts, now)
	// At capacity, shouldInitiate already prevents new requests. Do not retain
	// unlimited cooldown entries even if incoming peers disconnect rapidly.
	if _, exists := attempts[nmi]; exists || len(attempts) < maxRadioAttempts {
		attempts[nmi] = now
	}
}

// shouldInitiate reports whether this node must request the NDP: the device
// with the lower UUID initiates each edge, so the higher one only listens.
func shouldInitiate(self, peer meshid.Device, nmi string, active map[string]*radioLink, attempts map[string]time.Time, now time.Time) bool {
	expireRadioAttempts(attempts, now)
	if meshid.Compare(self, peer) >= 0 || sameUnderlay(self, peer) ||
		active[nmi] != nil || !shouldConnect(nmi, peer, active) || len(attempts) >= maxRadioAttempts {
		return false
	}
	if last, ok := attempts[nmi]; ok && now.Sub(last) < radioAttemptCooldown {
		return false
	}
	return true
}

func shouldAccept(self, peer meshid.Device, nmi string, active map[string]*radioLink) bool {
	return meshid.Compare(peer, self) < 0 && !sameUnderlay(self, peer) && shouldConnect(nmi, peer, active)
}

// sameUnderlay reports two distinct devices whose last UUID bytes collide,
// which would give them the same NAN underlay address.
func sameUnderlay(a, b meshid.Device) bool {
	return a.UUID()[14] == b.UUID()[14] && a.UUID()[15] == b.UUID()[15]
}

func shouldConnect(nmi string, peer meshid.Device, active map[string]*radioLink) bool {
	if !validMAC(nmi) {
		return false
	}
	if _, err := underlayAddr(peer); err != nil {
		return false
	}
	for otherNMI, existing := range active {
		if otherNMI == nmi {
			if existing.peer.Device != peer {
				return false
			}
			continue
		}
		// One NDI has one neighbour entry per address. A second NMI claiming
		// that address/device must not replace an incumbent before TLS.
		if sameUnderlay(existing.peer.Device, peer) {
			return false
		}
	}
	return active[nmi] != nil || len(active) < 3
}

// NMI and NDI targets are individual six-byte addresses. They are passed
// verbatim to supplicant's colon-delimited hwaddr_aton control API; zero is
// its missing-parameter sentinel, not a usable target. Hex case is immaterial.
func validMAC(raw string) bool {
	mac, err := net.ParseMAC(raw)
	if err != nil || len(mac) != 6 || !strings.EqualFold(raw, mac.String()) || mac[0]&1 != 0 {
		return false
	}
	for _, octet := range mac {
		if octet != 0 {
			return true
		}
	}
	return false
}

func connectedPeer(v map[string]string, local string, active map[string]*radioLink) (radioPeer, bool) {
	nmi := v["peer"]
	// CONNECTED must complete a request/response we admitted on our own NDI.
	// Discovery SSI alone cannot create a worker or overwrite its identity.
	link := active[nmi]
	if link == nil {
		return radioPeer{}, false
	}
	if (link.peer.ID != "" && link.peer.ID != v["ndp_id"]) ||
		(link.peer.NDI != "" && link.peer.NDI != v["peer_ndi"]) {
		return radioPeer{}, false
	}
	peer := link.peer.Device
	if v["local_ndi"] != local || !shouldConnect(nmi, peer, active) || !validNDPID(v["ndp_id"]) || !validMAC(v["peer_ndi"]) {
		return radioPeer{}, false
	}
	return radioPeer{Device: peer, NMI: nmi, NDI: v["peer_ndi"], ID: v["ndp_id"]}, true
}

func disconnectedPeer(v map[string]string, local string, active map[string]*radioLink) (radioPeer, bool) {
	if v["local_ndi"] != "" && v["local_ndi"] != local {
		return radioPeer{}, false
	}
	link := active[v["peer"]]
	if link == nil || !link.matchesDisconnect(v) {
		return radioPeer{}, false
	}
	return radioPeer{Device: link.peer.Device, NMI: v["peer"], ID: link.peer.ID}, true
}

// runRadio runs all NDP responses on a persistent local control socket so it
// meets the firmware deadline. Discovery claims remain untrusted until QUIC TLS.
func runRadio(ctx context.Context, ident meshid.Device, connected chan<- radioPeer, health <-chan radioHealth, logger *zap.Logger) error {
	control, err := openRadio()
	if err != nil {
		return err
	}
	defer control.close()
	events, err := openRadio()
	if err != nil {
		return err
	}
	defer events.close()
	if _, err = events.command("ATTACH"); err != nil {
		return err
	}
	_ = events.conn.SetDeadline(time.Time{})
	rawEvents := make(chan string, 128)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go func() {
		defer close(rawEvents)
		buf := make([]byte, 16384)
		for {
			n, err := events.conn.Read(buf)
			if err != nil {
				return
			}
			select {
			case rawEvents <- string(buf[:n]):
			case <-readerDone:
				return
			}
		}
	}()
	iface, err := net.InterfaceByName(ndiName)
	if err != nil {
		return err
	}
	local := iface.HardwareAddr.String()
	defer drainRadio(control, rawEvents, local, "", logger)
	ssi := hex.EncodeToString([]byte("wendy-nan:2:" + ident.UUID().String()))
	socket, err := nanControlSocketIdentity()
	if err != nil {
		return err
	}
	discovery, err := startDiscovery(control, discoveryOwnerPath, ident, socket, ssi)
	if err != nil {
		return err
	}
	defer func() {
		if err := discovery.release(); err != nil {
			logger.Warn("NAN discovery cleanup failed", zap.Error(err))
		}
	}()
	pub, sub := discovery.publish, discovery.sub
	attempts := map[string]time.Time{}
	active := map[string]*radioLink{}
	lastProbe := time.Now()
	recovery := radioRecovery{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	emit := func(p radioPeer) {
		select {
		case connected <- p:
		case <-ctx.Done():
		}
	}
	for {
		var raw string
		select {
		case <-ctx.Done():
			return ctx.Err()
		case h := <-health:
			if l := active[h.Peer.NMI]; l != nil && l.peer == h.Peer {
				l.health(h.Up, time.Now())
				if h.Up && meshid.Compare(h.Peer.Device, ident) < 0 {
					recovery.hasLowerPeer = true
				}
			}
			continue
		case <-ticker.C:
			expireRadioAttempts(attempts, time.Now())
			anyHealthy := false
			for _, link := range active {
				anyHealthy = anyHealthy || link.healthy
			}
			recovery.observe(time.Now(), anyHealthy)
			if recovery.expired(time.Now()) {
				return fmt.Errorf("NAN data recovery: repeated replacement NDPs without authenticated traffic; cycling owned NAN session after staggered deadline")
			}
			if time.Since(lastProbe) >= 5*time.Second {
				if pong, err := control.command("PING"); err != nil || pong != "PONG" {
					return fmt.Errorf("NAN control connection lost: reply=%q err=%v", pong, err)
				}
				lastProbe = time.Now()
			}
			for nmi, link := range active {
				if !link.expired(time.Now()) {
					continue
				}
				logger.Warn("NAN stale link recovery", zap.String("peer", nmi), zap.String("device", link.peer.Device.UUID().String()))
				info, err := control.command("NAN_PEER_INFO " + nmi + " ndps")
				if err == nil {
					for _, ndp := range ownedNDPs(nmi, local, info) {
						_, err := control.command(fmt.Sprintf("NAN_NDP_TERMINATE peer_nmi=%s init_ndi=%s ndp_id=%s", ndp.peer, ndp.init, ndp.id))
						if err != nil {
							logger.Warn("NAN stale termination failed", zap.Error(err))
						}
					}
				}
				emit(radioPeer{Device: link.peer.Device, NMI: nmi, ID: link.peer.ID})
				delete(active, nmi)
				rememberRadioAttempt(attempts, nmi, time.Now())
			}
			continue
		case event, ok := <-rawEvents:
			if !ok {
				return fmt.Errorf("NAN event monitor closed")
			}
			raw = event
		}
		kind, v := parseEvent(raw)
		if strings.HasPrefix(kind, "NAN-NDP-") {
			logger.Info("NAN lifecycle event", zap.String("event", strings.TrimSpace(raw)))
		}
		switch kind {
		case "NAN-DISCOVERY-RESULT":
			if v["subscribe_id"] != sub {
				continue
			}
			peer, err := decodePresence(ident.Tenant(), v["ssi"])
			if err != nil || peer == ident {
				continue
			}
			if sameUnderlay(ident, peer) {
				logger.Warn("NAN underlay collision with peer", zap.String("peer", v["address"]), zap.String("device", peer.UUID().String()))
				continue
			}
			nmi := v["address"]
			if !validMAC(nmi) {
				continue
			}
			if !validHandle(v["publish_id"]) || !shouldInitiate(ident, peer, nmi, active, attempts, time.Now()) {
				continue
			}
			rememberRadioAttempt(attempts, nmi, time.Now())
			if _, err = control.command(fmt.Sprintf("NAN_NDP_REQUEST handle=%s ndi=%s peer_nmi=%s peer_id=%s ssi=%s", sub, ndiName, nmi, v["publish_id"], ssi)); err == nil {
				active[nmi] = &radioLink{peer: radioPeer{Device: peer, NMI: nmi}, since: time.Now()}
			}
		case "NAN-NDP-REQUEST":
			nmi := v["peer_nmi"]
			if v["publish_inst_id"] != pub {
				continue
			}
			peer, err := decodePresence(ident.Tenant(), v["ssi"])
			if err != nil || !validMAC(nmi) || !shouldAccept(ident, peer, nmi, active) {
				continue
			}
			if sameUnderlay(ident, peer) {
				logger.Warn("NAN underlay collision with peer", zap.String("peer", nmi), zap.String("device", peer.UUID().String()))
				continue
			}
			if !validMAC(v["init_ndi"]) {
				continue
			}
			if !validNDPID(v["ndp_id"]) {
				continue
			}
			// This provider owns one NDP per peer. A new request while an old
			// NDP exists usually means that the peer restarted. Do not replace
			// its established record with a pending boolean: that would lose
			// the old NDP's health deadline and leave its NDL/schedule behind.
			if old := active[nmi]; old != nil && old.peer.NDI != "" {
				_, rejectErr := control.command(fmt.Sprintf("NAN_NDP_RESPONSE reject peer_nmi=%s ndp_id=%s init_ndi=%s", nmi, v["ndp_id"], v["init_ndi"]))
				logger.Warn("NAN replacement waits for old NDP cleanup", zap.String("peer", nmi), zap.Error(rejectErr))
				old.health(false, time.Now())
				emit(radioPeer{Device: old.peer.Device, NMI: nmi, ID: old.peer.ID})
				continue
			}
			if _, err = control.command(fmt.Sprintf("NAN_NDP_RESPONSE accept handle=%s ndi=%s peer_nmi=%s ndp_id=%s init_ndi=%s ssi=%s", pub, ndiName, nmi, v["ndp_id"], v["init_ndi"], ssi)); err == nil {
				active[nmi] = &radioLink{peer: radioPeer{Device: peer, NMI: nmi, ID: v["ndp_id"]}, since: time.Now()}
			}
		case "NAN-NDP-COUNTER-REQUEST":
			nmi := v["peer_nmi"]
			peer, err := decodePresence(ident.Tenant(), v["ssi"])
			link := active[nmi]
			if err != nil || link == nil || meshid.Compare(link.peer.Device, ident) <= 0 || meshid.Compare(link.peer.Device, peer) != 0 || link.healthy || link.peer.NDI != "" {
				continue
			}
			cmd, err := counterResponse(local, sub, ssi, v)
			if err != nil {
				continue
			}
			if _, err = control.command(cmd); err != nil {
				logger.Warn("NAN counter proposal confirmation failed", zap.Error(err))
			} else {
				link.peer.ID = v["ndp_id"]
				logger.Info("NAN counter proposal confirmed", zap.String("peer", nmi), zap.String("ndp_id", v["ndp_id"]))
			}
		case "NAN-NDP-CONNECTED":
			p, ok := connectedPeer(v, local, active)
			if !ok {
				continue
			}
			if existing := active[p.NMI]; existing != nil && existing.peer == p {
				continue
			}
			active[p.NMI] = &radioLink{peer: p, since: time.Now()}
			recovery.connected()
			emit(p)
		case "NAN-NDP-DISCONNECTED":
			if p, ok := disconnectedPeer(v, local, active); ok {
				emit(p)
				delete(active, p.NMI)
			}
		}
	}
}
