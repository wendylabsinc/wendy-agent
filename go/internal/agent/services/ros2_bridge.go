package services

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/agent/foxglovebridge"
)

// ros2Bridge owns one long-lived wendy-ros2-bridge process per sidecar and
// multiplexes subscriptions/publishes over its stdin/stdout control protocol.
type ros2Bridge struct {
	rt   ROS2Runtime
	mu   sync.Mutex
	proc map[string]*bridgeProc // keyed by sidecar name
}

func newROS2Bridge(rt ROS2Runtime) *ros2Bridge {
	return &ros2Bridge{rt: rt, proc: map[string]*bridgeProc{}}
}

// bridgeProc is one running wendy-ros2-bridge process and its multiplexed
// subscriptions. All stdin writes and subs map access are guarded by mu.
type bridgeProc struct {
	stdin  *io.PipeWriter
	mu     sync.Mutex // serializes stdin writes + subs map + ready/distro
	nextID uint32
	subs   map[uint32]chan foxglovebridge.Message
	ready  chan struct{}
	distro string
	dead   chan struct{} // closed once the process has exited
}

// ensure starts the bridge for sc if not already running. Returns an error the
// caller treats as "fall back to the legacy path".
func (b *ros2Bridge) ensure(ctx context.Context, sc ros2SC) (*bridgeProc, error) {
	b.mu.Lock()
	if p, ok := b.proc[sc.name]; ok {
		b.mu.Unlock()
		return p, nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bridgeProc{
		stdin: inW,
		subs:  map[uint32]chan foxglovebridge.Message{},
		ready: make(chan struct{}),
		dead:  make(chan struct{}),
	}
	b.proc[sc.name] = p
	b.mu.Unlock()

	go func() {
		_, _ = b.rt.ExecROS2Stream(context.WithoutCancel(ctx), ROS2ExecOptions{
			DomainID:     sc.domainID,
			SidecarName:  sc.name,
			BridgeBinary: true,
		}, inR, outW, io.Discard)
		// Close both ends of the process's pipes: outW.Close() unblocks the
		// readLoop (EOF) below, and inR.Close() unblocks/fails any stdin
		// Write that is or will be pending on the now-dead process (writes to
		// a PipeWriter whose reader is closed return io.ErrClosedPipe instead
		// of blocking forever).
		outW.Close()
		inR.Close()
		close(p.dead)
		b.mu.Lock()
		delete(b.proc, sc.name)
		b.mu.Unlock()
	}()

	go p.readLoop(outR)
	return p, nil
}

func (p *bridgeProc) readLoop(r io.Reader) {
	var buf []byte
	for {
		fr, nb, err := foxglovebridge.ReadFrame(r, buf)
		buf = nb
		if err != nil {
			p.failAll()
			return
		}
		switch fr.Tag {
		case foxglovebridge.KindReady:
			distro, _, _ := foxglovebridge.ParseReady(fr.Body)
			p.mu.Lock()
			p.distro = distro
			select {
			case <-p.ready:
			default:
				close(p.ready)
			}
			p.mu.Unlock()
		case foxglovebridge.KindMessage:
			m, err := foxglovebridge.ParseMessage(fr.Body)
			if err != nil {
				continue
			}
			cp := make([]byte, len(m.CDR)) // copy: buf is reused next iteration
			copy(cp, m.CDR)
			m.CDR = cp
			p.mu.Lock()
			ch := p.subs[m.SubID]
			p.mu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default: // slow consumer: drop (freshest-sample-wins, matches CLI side)
				}
			}
		case foxglovebridge.KindSubError:
			subID, _, _ := foxglovebridge.ParseSubError(fr.Body)
			p.mu.Lock()
			if ch := p.subs[subID]; ch != nil {
				close(ch)
				delete(p.subs, subID)
			}
			p.mu.Unlock()
		}
	}
}

// failAll closes every live subscriber channel so blocked callers unblock
// when the bridge process dies (EOF or a read error on stdout).
func (p *bridgeProc) failAll() {
	p.mu.Lock()
	for id, ch := range p.subs {
		close(ch)
		delete(p.subs, id)
	}
	p.mu.Unlock()
}

// Subscribe starts (or reuses) the bridge for sc and registers a new
// subscription for topic/msgType. The returned channel delivers decoded
// MESSAGE frames until cancel is called or the bridge process dies (in which
// case the channel is closed). A non-nil error means the bridge is
// unavailable and the caller should fall back to the legacy path.
func (b *ros2Bridge) Subscribe(ctx context.Context, sc ros2SC, topic, msgType string) (<-chan foxglovebridge.Message, func(), error) {
	p, err := b.ensure(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan foxglovebridge.Message, 8)
	p.subs[id] = ch
	cmd := foxglovebridge.AppendSubscribe(nil, id, topic, msgType, foxglovebridge.QoSAuto)
	_, werr := p.stdin.Write(cmd)
	if werr != nil {
		delete(p.subs, id)
	}
	p.mu.Unlock()
	if werr != nil {
		return nil, nil, fmt.Errorf("bridge subscribe write: %w", werr)
	}
	cancel := func() {
		p.mu.Lock()
		if _, ok := p.subs[id]; ok {
			delete(p.subs, id)
			_, _ = p.stdin.Write(foxglovebridge.AppendUnsubscribe(nil, id))
		}
		p.mu.Unlock()
	}
	return ch, cancel, nil
}

// Publish starts (or reuses) the bridge for sc and sends a PUBLISH frame. A
// non-nil error means the bridge is unavailable and the caller should fall
// back to the legacy path.
func (b *ros2Bridge) Publish(ctx context.Context, sc ros2SC, topic, msgType string, cdr []byte) error {
	p, err := b.ensure(ctx, sc)
	if err != nil {
		return err
	}
	p.mu.Lock()
	_, werr := p.stdin.Write(foxglovebridge.AppendPublish(nil, topic, msgType, cdr))
	p.mu.Unlock()
	if werr != nil {
		return fmt.Errorf("bridge publish write: %w", werr)
	}
	return nil
}

// available reports whether sc's bridge process is running and has completed
// its READY handshake. Callers use this to decide whether to attempt the
// fast path at all before incurring a Subscribe/Publish round trip.
func (b *ros2Bridge) available(sc ros2SC) bool {
	b.mu.Lock()
	p, ok := b.proc[sc.name]
	b.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case <-p.dead:
		return false
	default:
	}
	select {
	case <-p.ready:
		return true
	default:
		return false
	}
}
