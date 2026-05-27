package services

import (
	"context"
	"sync"
)

// frameSlot is a single-slot, drop-oldest hand-off between a producer (the V4L2
// capture goroutine) and a consumer (the gRPC sender goroutine).
//
// put never blocks: it overwrites any frame the consumer has not yet taken, so a
// slow sender (blocked on gRPC flow control) cannot stall the camera capture
// loop. take always returns the freshest frame available; intermediate frames
// produced while the sender was busy are dropped. This is what keeps the agent
// from delivering a stale backlog late — it sends the newest frame and discards
// the rest.
type frameSlot struct {
	mu     sync.Mutex
	buf    []byte
	have   bool
	wakeup chan struct{} // capacity 1; signals take that a frame is available
}

func newFrameSlot() *frameSlot {
	return &frameSlot{wakeup: make(chan struct{}, 1)}
}

// put stores frame as the latest available frame, dropping any frame the
// consumer has not yet taken. It never blocks.
func (s *frameSlot) put(frame []byte) {
	s.mu.Lock()
	s.buf = frame
	s.have = true
	s.mu.Unlock()

	// Non-blocking signal: a full channel already means "frame available".
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

// take blocks until a frame is available or ctx is cancelled. It returns the
// freshest frame and true, or nil and false if ctx was cancelled before a frame
// arrived. A buffered frame is delivered even if ctx is already cancelled, so no
// captured frame is silently discarded.
func (s *frameSlot) take(ctx context.Context) ([]byte, bool) {
	for {
		s.mu.Lock()
		if s.have {
			frame := s.buf
			s.buf = nil
			s.have = false
			s.mu.Unlock()
			return frame, true
		}
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, false
		case <-s.wakeup:
			// Re-check under lock: the wakeup may be stale (a frame already
			// taken, or coalesced with a later put).
		}
	}
}
