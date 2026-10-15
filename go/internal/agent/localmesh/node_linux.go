//go:build linux

package localmesh

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	quic "github.com/quic-go/quic-go"
	"github.com/wendylabsinc/WendyOS/go/babel"
	"github.com/wendylabsinc/wendy/go/internal/shared/meshid"
)

type nodePeer struct {
	id      babel.LinkID
	device  meshid.Device
	conn    *quic.Conn
	tun     *os.File
	control chan ControlMessage
	sync    *Synchronizer
}
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
		return nil, errors.New("missing local-mesh credentials or agent port")
	}
	now := time.Now()
	derived, err := instanceRouterID()
	if err != nil {
		return nil, err
	}
	store, state, err := OpenStateStore(filepath.Join(dir, "routing-state.json"), credentials.Device, derived)
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
	n := &Node{Credentials: credentials, kernel: kernel, cache: cache, directory: directory, store: store, state: state, peers: map[babel.LinkID]*nodePeer{}, events: make(chan nodeEvent, 256), ctx: ctx, cancel: cancel, started: now,
		advertisement: Manifest{Version: 2, Device: credentials.Device.UUID(), Name: name, AgentPort: agentPort}}
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
	out.Routes = append([]babel.Route(nil), v.Routes...)
	out.Devices = append([]Manifest(nil), v.Devices...)
	return out
}

func (n *Node) refresh() {
	n.view.Store(&NodeSnapshot{Peers: len(n.peers), Devices: n.directory.Snapshot(time.Now()), Routes: n.routing.Snapshot().Routes})
}

func (n *Node) Run() error {
	defer n.cancel()
	defer func() {
		_ = n.Stop()
		for _, p := range n.peers {
			_ = p.conn.CloseWithError(0, "local-mesh stopping")
		}
		_ = n.kernel.Close()
		_ = n.cache.Flush(time.Now())
	}()
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
			n.refresh()
			if err := n.step(babel.Tick{}); err != nil {
				return err
			}
		case <-renew.C:
			if err := n.publish(); err != nil {
				return err
			}
		case <-reconcile.C:
			for _, p := range n.peers {
				n.queue(p, p.sync.Reconcile(time.Now()))
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
			return errors.New("local-mesh link capacity reached")
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
		if err = n.step(babel.AddLink{Link: babel.Link{ID: e.add.id, Local: local, Peer: peer, Cost: 256, MaxPacket: DatagramLimit - 1}}); err != nil {
			return err
		}
		n.queue(e.add, e.add.sync.Reconcile(time.Now()))
		return nil
	}
	p := n.peers[e.id]
	if p == nil {
		return nil
	}
	if e.remove {
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
			_ = p.conn.CloseWithError(1, "invalid directory message")
			return nil
		}
		n.queue(p, replies)
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

func (n *Node) queue(p *nodePeer, messages []ControlMessage) {
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
	defer conn.CloseWithError(0, "local-mesh link ended")
	stream, err := OpenControl(ctx, conn, n.Credentials.Device, peer)
	if err != nil {
		return err
	}
	p := &nodePeer{device: peer, conn: conn, control: make(chan ControlMessage, 128)}
	if err = n.request(ctx, nodeEvent{add: p}); err != nil {
		return err
	}
	defer n.request(n.ctx, nodeEvent{id: p.id, remove: true})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-n.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
		_ = conn.CloseWithError(0, "link stopped")
		_ = p.tun.Close()
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
				if err := WriteControl(stream, m); err != nil {
					return err
				}
			}
		}
	})
	worker(func() error {
		for {
			m, err := ReadControl(stream)
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
	worker(func() error { return n.readQUIC(ctx, p) })
	err = <-done
	cancel()
	wg.Wait()
	return err
}

// readTUN carries the mesh's IPv6 payload only. The mesh has no IPv4 identity, so
// nothing else may leave a TUN.
func (n *Node) readTUN(ctx context.Context, p *nodePeer) error {
	buf := make([]byte, TunnelMTU+1)
	var sequence uint32
	for {
		size, err := p.tun.Read(buf)
		if err != nil {
			return err
		}
		packet := buf[:size]
		if !validIP(packet) || packet[0]>>4 != 6 {
			continue
		}
		n.gate.RLock()
		allowed := !n.stopped && forwardLink(n.forwardRoutes, netip.AddrFrom16([16]byte(packet[24:40]))) == p.id
		if allowed {
			sequence++
			fragments, _ := EncodeIP(sequence, packet)
			for _, d := range fragments {
				_ = p.conn.SendDatagram(d)
			}
		}
		n.gate.RUnlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (n *Node) readQUIC(ctx context.Context, p *nodePeer) error {
	var assembler IPAssembler
	for {
		d, err := p.conn.ReceiveDatagram(ctx)
		if err != nil {
			return err
		}
		if len(d) == 0 || len(d) > DatagramLimit {
			return errors.New("invalid local-mesh datagram")
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
			if packet[0]>>4 != 6 {
				continue
			}
			n.gate.RLock()
			if !n.stopped {
				_, err = p.tun.Write(packet)
			}
			n.gate.RUnlock()
			if err != nil {
				return err
			}
		default:
			return errors.New("unknown local-mesh datagram kind")
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
	return p.conn.SendDatagram(append([]byte{PacketBabel}, d.Payload...))
}
