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
	SyncPort              = 43022
	syncALPN              = "wendy-mesh-catalog/1"
	bundleHintLifetime    = 30 * time.Minute
	maxBundleHintsPerPeer = 64
	maxBundleHintPeers    = 128
)

// Runtime synchronizes signed catalog records over authenticated direct mesh
// links. Its only dependency on the mesh node is a read-only snapshot
// callback, avoiding a localmesh -> meshcatalog import cycle. Records relay
// hop by hop, so a device reachable only through neighbours still converges.
// Run until the context is cancelled.
type Runtime struct {
	catalog     *Catalog
	snapshot    func() localmesh.NodeSnapshot
	mu          sync.Mutex
	peers       map[meshid.Device]*catalogPeer
	bundleHints map[meshid.Device]map[string]time.Time
	tickets     *localmesh.TicketStore
	dialing     map[meshid.Device]bool
	closed      bool
}

type catalogPeer struct {
	conn net.Conn
	out  chan SignedRecord
}

func NewRuntime(catalog *Catalog, snapshot func() localmesh.NodeSnapshot) (*Runtime, error) {
	if catalog == nil || snapshot == nil {
		return nil, errors.New("missing mesh catalog runtime dependency")
	}
	return &Runtime{catalog: catalog, snapshot: snapshot, peers: map[meshid.Device]*catalogPeer{},
		bundleHints: map[meshid.Device]map[string]time.Time{}, dialing: map[meshid.Device]bool{},
		tickets: catalog.creds.ServerTicketStore("catalog", "catalog-tls")}, nil
}

func (r *Runtime) knownBundles(device meshid.Device, now time.Time) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var known []string
	for fp, written := range r.bundleHints[device] {
		if now.Sub(written) >= bundleHintLifetime || now.Before(written) {
			delete(r.bundleHints[device], fp)
			continue
		}
		known = append(known, fp)
	}
	if len(r.bundleHints[device]) == 0 {
		delete(r.bundleHints, device)
	}
	return known
}

func (r *Runtime) bundleWritten(device meshid.Device, bundle [][]byte, now time.Time) {
	fp := localmesh.Fingerprint(bundle)
	if len(fp) != 64 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	hints := r.bundleHints[device]
	if hints == nil {
		if len(r.bundleHints) >= maxBundleHintPeers {
			var oldestDevice meshid.Device
			var oldest time.Time
			for id, entries := range r.bundleHints {
				for _, written := range entries {
					if oldest.IsZero() || written.Before(oldest) {
						oldestDevice, oldest = id, written
					}
				}
			}
			delete(r.bundleHints, oldestDevice)
		}
		hints = make(map[string]time.Time)
		r.bundleHints[device] = hints
	}
	if _, exists := hints[fp]; !exists && len(hints) >= maxBundleHintsPerPeer {
		var oldest string
		var when time.Time
		for name, written := range hints {
			if oldest == "" || written.Before(when) {
				oldest, when = name, written
			}
		}
		delete(hints, oldest)
	}
	hints[fp] = now
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
	if state.NegotiatedProtocol != syncALPN || len(state.PeerCertificates) == 0 {
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
	r.session(ctx, id.Device, conn)
}

func (r *Runtime) discover(ctx context.Context, now time.Time) {
	view := r.snapshot()
	// Dial only authenticated direct links, and only toward the half-space
	// above this device's UUID, so each pair runs exactly one session.
	for _, link := range view.Links {
		device := link.Device
		if meshid.Compare(device, r.catalog.device) <= 0 || !eligibleSnapshot(view, device, now) {
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

func (r *Runtime) outbound(ctx context.Context, device meshid.Device) {
	defer func() { r.mu.Lock(); delete(r.dialing, device); r.mu.Unlock() }()
	self := r.catalog.device.MeshAddress()
	peer := device.MeshAddress()
	conf, err := r.catalog.creds.PeerTLSWithTickets(device, syncALPN, "catalog-tls")
	if err != nil {
		return
	}
	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dialer := &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(self.AsSlice())}}
	raw, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(peer.String(), fmt.Sprint(SyncPort)))
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
	r.session(ctx, device, conn)
}

func (r *Runtime) session(ctx context.Context, device meshid.Device, conn net.Conn) {
	// A failed peer session must also cancel its reader when the decoded
	// message queue is full. Closing conn alone cannot wake a channel send,
	// and the runtime context normally survives many peer reconnects.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	peer := &catalogPeer{conn: conn, out: make(chan SignedRecord, 128)}
	r.mu.Lock()
	if r.closed || r.peers[device] != nil {
		r.mu.Unlock()
		return
	}
	r.peers[device] = peer
	r.mu.Unlock()
	defer func() {
		_ = conn.Close()
		r.mu.Lock()
		if r.peers[device] == peer {
			delete(r.peers, device)
		}
		r.mu.Unlock()
	}()
	syncer := NewSynchronizer(r.catalog, r.catalog.cache)
	syncer.SeedKnownBundles(r.knownBundles(device, time.Now()))
	in := make(chan Message, 16)
	readErr := make(chan error, 1)
	out := make(chan Message)
	writeErr := make(chan error, 1)
	readDone, writeDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			m, err := ReadMessage(conn)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case in <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer close(writeDone)
		for m := range out {
			_ = conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
			if err := WriteMessage(conn, m); err != nil {
				writeErr <- err
				return
			}
			if m.Kind == "bundle" {
				r.bundleWritten(device, m.Bundle, time.Now())
			}
		}
	}()
	defer func() {
		cancel()
		_ = conn.Close()
		close(out)
		<-readDone
		<-writeDone
	}()
	// Keep only one catalog-bounded record snapshot, feeding its frames
	// incrementally. A valid initial catalog may exceed the small wire queue.
	initial := r.catalog.Records(time.Now())
	initialQueued := false
	var queue legacyQueue
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if len(queue.messages) == 0 && !initialQueued {
			for len(initial) > 0 && len(queue.messages) == 0 {
				record := initial[0]
				initial[0] = SignedRecord{}
				initial = initial[1:]
				if queue.add(syncer.Record(record, time.Now())...) != nil {
					return
				}
			}
			if len(initial) == 0 {
				initial = nil
				initialQueued = true
			}
		}
		var next chan Message
		var first Message
		if len(queue.messages) > 0 {
			next, first = out, queue.messages[0].message
		}
		select {
		case <-ctx.Done():
			return
		case <-readErr:
			return
		case <-writeErr:
			return
		case next <- first:
			queue.pop()
		case <-ticker.C:
			if !r.eligible(device, time.Now()) {
				return
			}
			// Reconcile's temporary batch is bounded by catalog capacity, not
			// this byte limit. Reject overload atomically and repair on reconnect.
			// Do not reconcile ahead of an unfinished initial snapshot.
			if initialQueued && queue.add(syncer.Reconcile(time.Now())...) != nil {
				return
			}
		case w := <-peer.out:
			if queue.add(syncer.Record(w, time.Now())...) != nil {
				return
			}
		case m := <-in:
			replies, changed, err := syncer.Receive(m, time.Now())
			if err != nil {
				return
			}
			if queue.add(replies...) != nil {
				return
			}
			for _, w := range changed {
				r.Broadcast(w)
			}
		}
	}
}
