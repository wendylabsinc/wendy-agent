//go:build linux

package localmesh

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

type nodePeer struct {
	id         babel.LinkID
	device     meshid.Device
	cost       uint16
	link       nodeLink
	tun        *os.File
	control    chan ControlMessage
	sync       *Synchronizer
	replies    []ControlMessage
	replyBytes int
}

type nodeLink interface {
	io.Closer
	SendDatagram([]byte) error
	ReceiveDatagram(context.Context) ([]byte, error)
	WriteControl(ControlMessage) error
	ReadControl() (ControlMessage, error)
}

type quicNodeLink struct {
	conn   *quic.Conn
	stream *quic.Stream
}

func (l quicNodeLink) Close() error                   { return l.conn.CloseWithError(0, "wendy net link ended") }
func (l quicNodeLink) SendDatagram(data []byte) error { return l.conn.SendDatagram(data) }
func (l quicNodeLink) ReceiveDatagram(ctx context.Context) ([]byte, error) {
	return l.conn.ReceiveDatagram(ctx)
}
func (l quicNodeLink) WriteControl(m ControlMessage) error  { return WriteControl(l.stream, m) }
func (l quicNodeLink) ReadControl() (ControlMessage, error) { return ReadControl(l.stream) }

type nodeEvent struct {
	add     *nodePeer
	id      babel.LinkID
	remove  bool
	packet  []byte
	control *ControlMessage
	result  chan error
}

// Node serializes all Babel/directory mutations. Radio providers only establish
// authenticated QUIC connections and pass them to Attach; no NAN concepts appear
// in routing or service synchronization. Parallel links have distinct LinkIDs.
type Node struct {
	Credentials     *Credentials
	kernel          *Kernel
	cache           *IdentityCache
	directory       *Directory
	bundleHints     *bundleHintCache
	store           *StateStore
	state           PersistentState
	routing         *Routing
	peers           map[babel.LinkID]*nodePeer
	events          chan nodeEvent
	ctx             context.Context
	cancel          context.CancelFunc
	started         time.Time
	nextLink        babel.LinkID
	advertisement   Manifest
	view            atomic.Pointer[NodeSnapshot]
	gate            sync.RWMutex
	paused, stopped bool
	forwardRoutes   []babel.Route
}

func NewNode(parent context.Context, dir string, credentials *Credentials, name string, agentPort uint16) (*Node, error) {
	if credentials == nil || agentPort == 0 {
		return nil, errors.New("missing wendy net credentials or agent port")
	}
	now := time.Now()
	derived, err := instanceRouterID()
	if err != nil {
		return nil, err
	}
	store, state, err := OpenDeviceStateStore(filepath.Join(dir, "routing-state.json"), credentials.Device, derived)
	if err != nil {
		return nil, err
	}
	cache, err := OpenIdentityCache(filepath.Join(dir, "identity-cache.json"), DefaultCacheLimits(), credentials.Verify, now)
	if err != nil {
		return nil, err
	}
	if _, err = cache.Put(credentials.Certificate.Certificate, now); err != nil {
		return nil, err
	}
	directory, err := NewDirectory(credentials.Device.Tenant(), 1024, cache)
	if err != nil {
		return nil, err
	}
	kernel, err := NewKernel(credentials.Device)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	n := &Node{Credentials: credentials, kernel: kernel, cache: cache, directory: directory, bundleHints: newBundleHintCache(), store: store, state: state, peers: map[babel.LinkID]*nodePeer{}, events: make(chan nodeEvent, 256), ctx: ctx, cancel: cancel, started: now,
		advertisement: Manifest{Version: 1, Device: credentials.Device.UUID(), Name: name, AgentPort: agentPort}}
	ok := false
	defer func() {
		if !ok {
			cancel()
			_ = kernel.Close()
		}
	}()
	if err = directory.SetPersistence(state.Receipts, func(receipts []DirectoryReceipt) error {
		next := n.state
		next.Receipts = receipts
		if err := n.store.Save(next); err != nil {
			return err
		}
		n.state = next
		return nil
	}, now); err != nil {
		return nil, err
	}
	// The stored state pins the router ID for this device principal; a fresh
	// instance uses the derivation. Admission is the authenticated link, so
	// AcceptRoute never consults an origin router ID or a manifest.
	config := babel.Config{RouterID: babel.RouterID(state.RouterID), HelloInterval: time.Second, UpdateInterval: 4 * time.Second, AcceptRoute: func(_ babel.RouterID, prefix netip.Prefix) bool {
		return admittedPrefix(prefix)
	}}
	n.routing, err = NewRouting(config, state.Babel, n)
	if err != nil {
		return nil, err
	}
	if state.Babel != nil {
		n.nextLink = state.Babel.LastLink
	}
	n.view.Store(&NodeSnapshot{})
	ok = true
	return n, nil
}

func (n *Node) Snapshot() NodeSnapshot {
	v := n.view.Load()
	if v == nil {
		return NodeSnapshot{}
	}
	out := *v
	out.Links = append([]PeerLink(nil), v.Links...)
	for i := range out.Links {
		if q := out.Links[i].queue; q != nil {
			stats := q.snapshot()
			out.Links[i].SendQueue = &stats
		}
	}
	out.Routes = append([]babel.Route(nil), v.Routes...)
	out.Devices = append([]Manifest(nil), v.Devices...)
	return out
}

// HasCheaperLink lets an expensive carrier avoid duplicating a verified
// direct neighbor already present over TCP or NAN. It reads the immutable
// event-loop snapshot and does not block route processing.
func (n *Node) HasCheaperLink(device meshid.Device, cost uint16) bool {
	v := n.view.Load()
	return v != nil && v.HasCheaperLink(device, cost)
}

func (n *Node) refresh() {
	devices := n.directory.Snapshot(time.Now())
	links := make([]PeerLink, 0, len(n.peers))
	for _, p := range n.peers {
		link := PeerLink{Device: p.device, Cost: p.cost}
		if stream, ok := p.link.(*streamNodeLink); ok {
			link.queue = stream.ipPackets
		}
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool {
		if links[i].Device == links[j].Device {
			return links[i].Cost < links[j].Cost
		}
		return meshid.Compare(links[i].Device, links[j].Device) < 0
	})
	n.view.Store(&NodeSnapshot{Peers: len(n.peers), Links: links, Devices: devices, Routes: n.routing.Snapshot().Routes})
}

func (n *Node) Run() error {
	defer n.cancel()
	defer func() {
		_ = n.Stop()
		for _, p := range n.peers {
			p.sync.Close()
			_ = p.link.Close()
		}
		_ = n.kernel.Close()
		_ = n.cache.Flush(time.Now())
	}()
	// NetworkManager can race dummy creation before its unmanaged-device policy
	// takes effect. Verify the base state once before originating Babel routes,
	// then keep repairing it as the agent runs.
	if err := n.kernel.ReconcileBase(); err != nil {
		return err
	}
	if err := n.step(babel.Originate{Prefix: netip.PrefixFrom(n.Credentials.Device.MeshAddress(), 128)}); err != nil {
		return err
	}
	if err := n.publish(); err != nil {
		return err
	}
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	maintenance := time.NewTicker(time.Second)
	defer maintenance.Stop()
	renew := time.NewTicker(20 * time.Second)
	defer renew.Stop()
	reconcile := time.NewTicker(30 * time.Second)
	defer reconcile.Stop()
	for {
		delay := time.Second
		if deadline, ok := n.routing.NextDeadline(); ok {
			delay = max(time.Millisecond, deadline-time.Since(n.started))
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		select {
		case <-n.ctx.Done():
			return n.ctx.Err()
		case <-timer.C:
			if err := n.step(babel.Tick{}); err != nil {
				return err
			}
		case <-maintenance.C:
			n.gate.Lock()
			err := n.kernel.ReconcileBase()
			n.gate.Unlock()
			if err != nil {
				return err
			}
			n.refresh()
			if err := n.step(babel.Tick{}); err != nil {
				return err
			}
			// Continue an unfinished repair pass as the bounded control queue
			// drains. This uses the existing maintenance tick, not a new scan
			// interval, and stops once the pass completes.
			for _, p := range n.peers {
				replies, changed, err := p.sync.ContinueCertificates(time.Now())
				if err != nil {
					_ = p.link.Close()
					continue
				}
				if !n.queueReplies(p, replies) {
					_ = p.link.Close()
					continue
				}
				for _, record := range changed {
					for id, dest := range n.peers {
						if id != p.id {
							n.queue(dest, dest.sync.Record(record, time.Now()))
						}
					}
				}
				n.continueReconcilePeer(p, time.Now())
			}
		case <-renew.C:
			if err := n.publish(); err != nil {
				return err
			}
		case <-reconcile.C:
			for _, p := range n.peers {
				n.reconcilePeer(p, time.Now())
			}
		case event := <-n.events:
			err := n.handle(event)
			if event.result != nil {
				event.result <- err
			}
			if err != nil && event.result == nil {
				return err
			}
			if errors.Is(err, babel.ErrStopped) {
				return err
			}
		}
	}
}

func (n *Node) step(event babel.Event) error {
	if err := n.routing.Step(time.Since(n.started), event); err != nil {
		return errors.Join(babel.ErrStopped, err)
	}
	n.refresh()
	return nil
}

func (n *Node) handle(e nodeEvent) error {
	if e.add != nil {
		if len(n.peers) >= 64 {
			return errors.New("wendy net link capacity reached")
		}
		if e.add.device.Principal() == n.Credentials.Device.Principal() {
			return errors.New("self link")
		}
		n.nextLink++
		e.add.id = n.nextLink
		tun, err := n.kernel.AddPeer(e.add.id, e.add.device)
		if err != nil {
			return err
		}
		e.add.tun = tun
		e.add.sync = NewSynchronizer(n.directory, n.cache)
		n.peers[e.add.id] = e.add
		// Both ends derive the same complementary pair from the full UUIDs.
		local, peer, err := meshid.LinkLocalAddresses(n.Credentials.Device, e.add.device)
		if err != nil {
			return err
		}
		if err = n.step(babel.AddLink{Link: babelLinkForPeer(e.add, local, peer)}); err != nil {
			return err
		}
		n.reconcilePeer(e.add, time.Now())
		return nil
	}
	p := n.peers[e.id]
	if p == nil {
		return nil
	}
	if e.remove {
		p.sync.Close()
		if err := n.step(babel.RemoveLink{ID: e.id}); err != nil {
			return err
		}
		delete(n.peers, e.id)
		_ = n.kernel.RemovePeer(e.id)
		n.refresh()
		return nil
	}
	if e.packet != nil {
		return n.step(babel.Receive{ID: e.id, Packet: e.packet})
	}
	if e.control != nil {
		replies, changed, err := p.sync.Receive(*e.control, time.Now())
		if err != nil {
			_ = p.link.Close()
			return nil
		}
		if !n.queueReplies(p, replies) {
			_ = p.link.Close()
			return nil
		}
		for _, record := range changed {
			for id, dest := range n.peers {
				if id != p.id {
					n.queue(dest, dest.sync.Record(record, time.Now()))
				}
			}
		}
		n.refresh()
	}
	return nil
}

func (n *Node) reconcilePeer(p *nodePeer, now time.Time) {
	n.queue(p, p.sync.ReconcileBounded(now, cap(p.control)-len(p.control)))
}

func (n *Node) continueReconcilePeer(p *nodePeer, now time.Time) {
	n.queue(p, p.sync.ContinueReconcile(now, cap(p.control)-len(p.control)))
}

func (n *Node) queue(p *nodePeer, messages []ControlMessage) {
	n.flushReplies(p)
	if len(p.replies) != 0 {
		return // Periodic reconciliation repairs bulk gossip after replies drain.
	}
	for _, m := range messages {
		select {
		case p.control <- m:
		default: /* periodic reconciliation repairs congestion */
		}
	}
}

func (n *Node) publish() error {
	principal := n.Credentials.Device.Principal()
	if _, _, ok := n.cache.Get(principal, time.Now()); !ok {
		if _, err := n.cache.Put(n.Credentials.Certificate.Certificate, time.Now()); err != nil {
			return err
		}
	}
	if n.state.Revision == ^uint64(0) {
		return errors.New("origin revision exhausted")
	}
	n.state.Revision++
	if err := n.store.Save(n.state); err != nil {
		return err
	}
	m := n.advertisement
	now := time.Now()
	m.Revision = n.state.Revision
	m.Issued = now.UnixMilli()
	m.Expires = now.Add(time.Minute).UnixMilli()
	w, err := SignManifest(m, n.Credentials.Certificate.Certificate, n.Credentials.Signer, now)
	if err != nil {
		return err
	}
	if _, err = n.directory.Accept(w, now); err != nil {
		return err
	}
	for _, p := range n.peers {
		n.queue(p, p.sync.Record(w, now))
	}
	n.refresh()
	return nil
}

func (n *Node) request(ctx context.Context, e nodeEvent) error {
	e.result = make(chan error, 1)
	select {
	case n.events <- e:
	case <-ctx.Done():
		return ctx.Err()
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
	select {
	case err := <-e.result:
		return err
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
}

// Attach owns the authenticated connection until return. No kernel interface is
// created until protocol negotiation succeeds. Caller must use Credentials.PeerTLS
// so the connection is already pinned to this peer's device principal.
func (n *Node) Attach(ctx context.Context, peer meshid.Device, conn *quic.Conn) error {
	return n.AttachWithCost(ctx, peer, conn, 256)
}

// AttachWithCost adds an authenticated carrier link with its Babel metric.
// TCP uses 256, NAN 512, and slower carriers may supply a higher cost.
func (n *Node) AttachWithCost(ctx context.Context, peer meshid.Device, conn *quic.Conn, cost uint16) error {
	defer conn.CloseWithError(0, "wendy net link ended")
	if err := n.verifyPeerCertificates(peer, conn.ConnectionState().TLS.PeerCertificates); err != nil {
		return err
	}
	stream, err := OpenControl(ctx, conn, n.Credentials.Device, peer)
	if err != nil {
		return err
	}
	return n.attachLink(ctx, peer, cost, quicNodeLink{conn, stream})
}

// AttachStream admits a mutually authenticated TLS 1.3 byte stream. The
// carrier must verify the enrolled peer certificate before calling this method.
func (n *Node) AttachStream(ctx context.Context, peer meshid.Device, conn net.Conn, cost uint16) error {
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return errors.New("wendy net stream requires TLS")
	}
	if err := n.verifyPeerCertificates(peer, tlsConn.ConnectionState().PeerCertificates); err != nil {
		return err
	}
	if err := OpenStreamHello(ctx, conn, n.Credentials.Device, peer); err != nil {
		return err
	}
	return n.attachLink(ctx, peer, cost, newStreamNodeLink(conn))
}

func (n *Node) verifyPeerCertificates(peer meshid.Device, certs []*x509.Certificate) error {
	if len(certs) == 0 || len(certs) > 16 {
		return errors.New("wendy net peer certificate missing")
	}
	chain := make([][]byte, len(certs))
	for i, cert := range certs {
		chain[i] = cert.Raw
	}
	id, err := n.Credentials.Verify(chain, time.Now())
	if err != nil {
		return err
	}
	if id.Device != peer {
		return errors.New("wendy net peer certificate does not match expected device")
	}
	if n.cache != nil {
		// The authenticated handshake already supplied these exact bytes.
		// Retain them for descriptors rather than requesting the peer's leaf
		// and issuers again on the control channel.
		n.cache.rememberCertificates(chain, id.NotAfter, time.Now())
	}
	return nil
}

func (n *Node) attachLink(ctx context.Context, peer meshid.Device, cost uint16, link nodeLink) error {
	if cost == 0 || cost == ^uint16(0) {
		return errors.New("invalid wendy net link cost")
	}
	p := &nodePeer{device: peer, cost: cost, link: link, control: make(chan ControlMessage, 128)}
	if err := n.request(ctx, nodeEvent{add: p}); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-n.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
		_ = link.Close()
	}()
	var wg sync.WaitGroup
	done := make(chan error, 4)
	worker := func(f func() error) { wg.Add(1); go func() { defer wg.Done(); done <- f() }() }
	worker(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case m := <-p.control:
				if err := link.WriteControl(m); err != nil {
					return err
				}
				if m.Kind == "bundle" {
					n.bundleHints.Written(p.device, m.Bundle, time.Now())
				}
			}
		}
	})
	worker(func() error {
		for {
			m, err := link.ReadControl()
			if err != nil {
				return err
			}
			select {
			case n.events <- nodeEvent{id: p.id, control: &m}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	worker(func() error { return n.readTUN(ctx, p) })
	worker(func() error { return n.readLink(ctx, p) })
	err := <-done
	// Keep the TUN alive while Babel withdraws its routes. Closing its last
	// file descriptor first destroys the kernel link, making route deletion
	// fail with ENODEV and stopping the shared routing engine.
	removeErr := n.request(n.ctx, nodeEvent{id: p.id, remove: true})
	cancel()
	// Kernel.RemovePeer closes the TUN after route withdrawal. If node
	// cancellation interrupted the request, Node.Run's Kernel.Close owns it
	// instead: the event loop may still be finishing a FIB operation. Closing
	// the descriptor here would destroy its link before that operation ends.
	wg.Wait()
	return errors.Join(err, removeErr)
}

// readTUN carries IPv6 payload along the selected mesh route.
func (n *Node) readTUN(ctx context.Context, p *nodePeer) error {
	buf := make([]byte, TunnelMTU+1)
	var sequence uint32
	for {
		size, err := p.tun.Read(buf)
		if err != nil {
			return err
		}
		packet := buf[:size]
		if !validIP(packet) {
			continue
		}
		n.gate.RLock()
		allowed := !n.stopped && tunCarries(n, p.id, packet)
		n.gate.RUnlock()
		if allowed {
			sequence++
			if talker, ok := p.link.(interface{ noteTalker([]byte, bool) }); ok {
				talker.noteTalker(packet, true)
			}
			if stream, ok := p.link.(interface{ SendIP(uint32, []byte) error }); ok {
				if err := stream.SendIP(sequence, packet); err != nil {
					_ = p.link.Close()
					return err
				}
			} else {
				fragments, _ := EncodeIP(sequence, packet)
				for _, d := range fragments {
					if err := p.link.SendDatagram(d); err != nil {
						_ = p.link.Close()
						return err
					}
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (n *Node) readLink(ctx context.Context, p *nodePeer) error {
	var assembler IPAssembler
	for {
		d, err := p.link.ReceiveDatagram(ctx)
		if err != nil {
			return err
		}
		if len(d) == 0 || len(d) > DatagramLimit {
			return errors.New("invalid wendy net datagram")
		}
		switch d[0] {
		case PacketBabel:
			select {
			case n.events <- nodeEvent{id: p.id, packet: append([]byte(nil), d[1:]...)}:
			case <-ctx.Done():
				return ctx.Err()
			}
		case PacketIP:
			packet, err := assembler.Receive(d, time.Now())
			if err != nil {
				return err
			}
			if packet == nil {
				continue
			}
			switch packet[0] >> 4 {
			case 4, 6:
			default:
				continue
			}
			if stream, ok := p.link.(*streamNodeLink); ok && stream.diagIP && packet[0]>>4 == 6 && len(packet) >= 256 {
				id := binary.BigEndian.Uint32(d[1:5])
				if id%16 == 0 {
					log.Printf("wendy net BLE IP event=receive peer=%s packet_id=%d source=%s bytes=%d ts_unix_ns=%d", p.device.UUID(), id, net.IP(packet[8:24]), len(packet), time.Now().UnixNano())
				}
			}
			n.gate.RLock()
			if !n.stopped && linkAdmits(n, packet) {
				if talker, ok := p.link.(interface{ noteTalker([]byte, bool) }); ok {
					talker.noteTalker(packet, false)
				}
				_, err = p.tun.Write(packet)
			}
			n.gate.RUnlock()
			if err != nil {
				return err
			}
		default:
			return errors.New("unknown wendy net datagram kind")
		}
	}
}

func forwardLink(routes []babel.Route, dst netip.Addr) babel.LinkID {
	best := -1
	var link babel.LinkID
	for _, r := range routes {
		if r.Prefix.Contains(dst) && r.Prefix.Bits() > best {
			best = r.Prefix.Bits()
			link = r.Link
			if r.Local || r.Unreachable {
				link = 0
			}
		}
	}
	return link
}

// tunCarries admits IPv6 payload only along its selected Babel route. IPv4
// internet traffic travels inside the IPv6 sharing tunnel, never as raw mesh IP.
func tunCarries(n *Node, id babel.LinkID, packet []byte) bool {
	return len(packet) >= 40 && packet[0]>>4 == 6 && id != 0 &&
		forwardLink(n.forwardRoutes, netip.AddrFrom16([16]byte(packet[24:40]))) == id
}

// linkAdmits permits IPv6 from an admitted peer. Kernel routing and the sharing
// tunnel policy govern delivery; raw IPv4 is outside the mesh transport contract.
func linkAdmits(_ *Node, packet []byte) bool {
	return len(packet) >= 40 && packet[0]>>4 == 6
}

func (n *Node) Apply(routes []babel.Route) error {
	n.gate.Lock()
	n.paused = true
	if err := n.kernel.Apply(routes); err != nil {
		return err
	}
	n.forwardRoutes = append([]babel.Route(nil), routes...)
	return nil
}
func (n *Node) Persist(cp babel.Checkpoint) error { n.state.Babel = &cp; return n.store.Save(n.state) }
func (n *Node) Resume() error {
	if n.paused {
		n.paused = false
		n.gate.Unlock()
	}
	return nil
}
func (n *Node) Stop() error {
	if !n.paused {
		n.gate.Lock()
	}
	n.stopped = true
	err := n.kernel.Stop()
	n.paused = false
	n.gate.Unlock()
	return err
}
func (n *Node) Send(d babel.Datagram) error {
	p := n.peers[d.Link]
	if p == nil {
		return fmt.Errorf("missing link %d", d.Link)
	}
	if err := p.link.SendDatagram(append([]byte{PacketBabel}, d.Payload...)); err != nil {
		_ = p.link.Close()
		// A failed carrier must withdraw its link, not stop the shared node.
		return nil
	}
	return nil
}
