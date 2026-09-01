package commands

import (
	"context"
	"testing"
	"time"
)

// waitDone reports whether ctx becomes done within a short grace period —
// AfterFunc-driven cancellation propagates asynchronously.
func waitDone(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// The bug this contract exists for: reconnect helpers cancel their per-attempt
// dial context as soon as the connection is returned, and the broker tunnel
// stream opened on that context died with it — every reconnected cloud
// connection was dead on arrival ("error reading from server: EOF" right after
// a successful liveness probe). After handoff, the tunnel context must survive
// the dial context's cancellation.
func TestDetachedTunnelContextSurvivesDialCancelAfterHandoff(t *testing.T) {
	dialCtx, dialCancel := context.WithCancel(context.Background())
	tunnelCtx, handoff, cancel := detachedTunnelContext(dialCtx)
	defer cancel()

	handoff()
	dialCancel()

	// Deterministic given handoff() returned before dialCancel(): the
	// dial-cancel link is severed synchronously, so give propagation no grace.
	select {
	case <-tunnelCtx.Done():
		t.Fatal("tunnel context died with the dial context despite handoff")
	case <-time.After(50 * time.Millisecond):
	}
}

// Before handoff the dial is still in flight, so cancelling the dial context
// must take the tunnel context down with it — an abandoned dial attempt must
// not leak a live broker stream.
func TestDetachedTunnelContextDiesWithDialCtxBeforeHandoff(t *testing.T) {
	dialCtx, dialCancel := context.WithCancel(context.Background())
	tunnelCtx, _, cancel := detachedTunnelContext(dialCtx)
	defer cancel()

	dialCancel()
	if !waitDone(tunnelCtx) {
		t.Fatal("tunnel context survived dial-context cancellation before handoff")
	}
}

// cancel is the connection's Close path: it must end the tunnel context even
// after handoff severed the dial-context link.
func TestDetachedTunnelContextCancelEndsItAfterHandoff(t *testing.T) {
	tunnelCtx, handoff, cancel := detachedTunnelContext(context.Background())

	handoff()
	cancel()
	if !waitDone(tunnelCtx) {
		t.Fatal("tunnel context survived its own cancel")
	}
}
