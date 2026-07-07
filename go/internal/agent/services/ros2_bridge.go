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
// subscriptions.
//
// Two separate mutexes guard disjoint concerns so that a stalled wire write
// can never block readLoop's per-frame fan-out lookups:
//
//   - bookMu guards subs/nextID/ready/distro (bookkeeping only). It is held
//     only for brief in-memory operations and is NEVER held across I/O.
//   - writeMu serializes stdin.Write calls only (so concurrent
//     Subscribe/Publish/cancel writers don't interleave frame bytes on the
//     wire). readLoop never acquires writeMu, so a write that blocks (e.g.
//     because the subprocess is hung and never reads stdin) cannot stall
//     delivery to any other live subscription.
type bridgeProc struct {
	stdin   *io.PipeWriter
	writeMu sync.Mutex // serializes stdin writes only; readLoop never touches this
	bookMu  sync.Mutex // guards subs map, nextID, ready-close-once, distro
	nextID  uint32
	subs    map[uint32]chan foxglovebridge.Message
	ready   chan struct{}
	distro  string
	dead    chan struct{} // closed once the process has exited
}

// writeFrame writes frame to stdin, serialized against other writers via
// writeMu, without ever holding bookMu. It returns promptly with an error if
// ctx is cancelled or the process dies before the write completes, so a
// caller can never block forever on a stalled/hung subprocess. Note: if ctx
// fires first, the underlying write may still be in flight in the
// background — it will complete (successfully or with an error) once the
// pipe unblocks, which is guaranteed to happen on process death (see
// ensure's exit goroutine, which closes both pipe ends).
func (p *bridgeProc) writeFrame(ctx context.Context, frame []byte) error {
	done := make(chan error, 1)
	go func() {
		p.writeMu.Lock()
		_, err := p.stdin.Write(frame)
		p.writeMu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-p.dead:
		return fmt.Errorf("bridge process is dead")
	}
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
		// of blocking forever). p.stdin (inW) is also closed for symmetry —
		// inR.Close() already unblocks pending/future Writes via
		// io.ErrClosedPipe, but closing the write end too makes the pipe pair
		// fully torn down rather than leaving inW technically open.
		outW.Close()
		inR.Close()
		_ = p.stdin.Close()
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
			p.bookMu.Lock()
			p.distro = distro
			select {
			case <-p.ready:
			default:
				close(p.ready)
			}
			p.bookMu.Unlock()
		case foxglovebridge.KindMessage:
			m, err := foxglovebridge.ParseMessage(fr.Body)
			if err != nil {
				continue
			}
			cp := make([]byte, len(m.CDR)) // copy: buf is reused next iteration
			copy(cp, m.CDR)
			m.CDR = cp
			p.bookMu.Lock()
			ch := p.subs[m.SubID]
			p.bookMu.Unlock()
			if ch != nil {
				select {
				case ch <- m:
				default: // slow consumer: drop (freshest-sample-wins, matches CLI side)
				}
			}
		case foxglovebridge.KindSubError:
			subID, _, _ := foxglovebridge.ParseSubError(fr.Body)
			p.bookMu.Lock()
			if ch := p.subs[subID]; ch != nil {
				close(ch)
				delete(p.subs, subID)
			}
			p.bookMu.Unlock()
		}
	}
}

// failAll closes every live subscriber channel so blocked callers unblock
// when the bridge process dies (EOF or a read error on stdout).
func (p *bridgeProc) failAll() {
	p.bookMu.Lock()
	for id, ch := range p.subs {
		close(ch)
		delete(p.subs, id)
	}
	p.bookMu.Unlock()
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
	p.bookMu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan foxglovebridge.Message, 8)
	p.subs[id] = ch
	p.bookMu.Unlock()

	cmd := foxglovebridge.AppendSubscribe(nil, id, topic, msgType, foxglovebridge.QoSAuto)
	if werr := p.writeFrame(ctx, cmd); werr != nil {
		// Roll back the registration. Note the write itself may still be in
		// flight in the background (if werr came from ctx/dead firing before
		// the write completed) — see writeFrame's doc comment.
		p.bookMu.Lock()
		delete(p.subs, id)
		p.bookMu.Unlock()
		return nil, nil, fmt.Errorf("bridge subscribe write: %w", werr)
	}
	cancel := func() {
		p.bookMu.Lock()
		_, ok := p.subs[id]
		if ok {
			delete(p.subs, id)
		}
		p.bookMu.Unlock()
		if !ok {
			return
		}
		// Fire-and-forget: cancel takes no ctx, and unsubscribe write errors
		// are inconsequential (the local bookkeeping removal above is what
		// actually matters to the caller). Run off the caller's goroutine so
		// cancel() itself can never block, even if the process is stalled.
		frame := foxglovebridge.AppendUnsubscribe(nil, id)
		go func() {
			p.writeMu.Lock()
			_, _ = p.stdin.Write(frame)
			p.writeMu.Unlock()
		}()
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
	if werr := p.writeFrame(ctx, foxglovebridge.AppendPublish(nil, topic, msgType, cdr)); werr != nil {
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
