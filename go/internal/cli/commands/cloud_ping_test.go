package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

// fakePingSession answers every echo request immediately. It selects on ctx
// everywhere it can block so a cancelled test context always unblocks both
// sendEcho and recv, instead of leaking a goroutine parked on an idle channel
// past test end (see fakeDatagramSession in cloud_datagram_test.go and
// fakeFrameStream in go/internal/agent/services/datagram_relay_test.go for
// the same pattern).
type fakePingSession struct {
	ctx     context.Context
	mu      sync.Mutex
	replies chan tunnelframe.Frame
	drop    bool // when true, swallow requests (packet loss)
}

func newFakePingSession(ctx context.Context) *fakePingSession {
	return &fakePingSession{ctx: ctx, replies: make(chan tunnelframe.Frame, 16)}
}
func (f *fakePingSession) sendEcho(req *tunnelframe.IcmpEchoRequest) error {
	f.mu.Lock()
	drop := f.drop
	f.mu.Unlock()
	if drop {
		return nil
	}
	reply := tunnelframe.Frame{IcmpReply: &tunnelframe.IcmpEchoReply{
		Identifier:      req.Identifier,
		Sequence:        req.Sequence,
		Payload:         req.Payload,
		OriginateUnixNs: req.OriginateUnixNs,
		AgentUnixNs:     uint64(time.Now().UnixNano()),
	}}
	select {
	case f.replies <- reply:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakePingSession) recv() (tunnelframe.Frame, error) {
	select {
	case fr, ok := <-f.replies:
		if !ok {
			return tunnelframe.Frame{}, io.EOF
		}
		return fr, nil
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}

// failingPingSession never delivers a reply; its recv() returns recvErr once
// a request has been sent, simulating a genuine transport failure
// (PermissionDenied, Unauthenticated, mesh-disabled, ...) rather than silence.
type failingPingSession struct {
	ctx     context.Context
	recvErr error
	sent    chan struct{}
	once    sync.Once
}

func newFailingPingSession(ctx context.Context, recvErr error) *failingPingSession {
	return &failingPingSession{ctx: ctx, recvErr: recvErr, sent: make(chan struct{})}
}
func (f *failingPingSession) sendEcho(*tunnelframe.IcmpEchoRequest) error {
	f.once.Do(func() { close(f.sent) })
	return nil
}
func (f *failingPingSession) recv() (tunnelframe.Frame, error) {
	select {
	case <-f.sent:
		return tunnelframe.Frame{}, f.recvErr
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}

func TestRunPingLoopCountsReplies(t *testing.T) {
	var out bytes.Buffer
	stats := runPingLoop(context.Background(), newFakePingSession(t.Context()), "device-1", 3, time.Millisecond, &out)
	if stats.Sent != 3 || stats.Received != 3 {
		t.Fatalf("stats = %+v, want 3 sent / 3 received", stats)
	}
	if stats.Min <= 0 || stats.Max < stats.Min || stats.Avg < stats.Min {
		t.Fatalf("implausible RTTs: %+v", stats)
	}
	if got := strings.Count(out.String(), "seq="); got != 3 {
		t.Fatalf("printed %d replies, want 3:\n%s", got, out.String())
	}
}

// TestRunPingLoopCountsLoss exercises the real "no replies" exit path: a
// finite count against a session that swallows every echo must return on its
// own once the grace window after the last send elapses — no ctx cancel
// backstop involved. runPingLoop is given context.Background() (never
// cancelled); only the safety net around the test itself has a deadline, and
// it fails the test (rather than silently making it pass) if the loop hangs.
func TestRunPingLoopCountsLoss(t *testing.T) {
	session := newFakePingSession(t.Context())
	session.drop = true
	var out bytes.Buffer

	statsCh := make(chan pingStats, 1)
	go func() {
		statsCh <- runPingLoop(context.Background(), session, "device-1", 2, 10*time.Millisecond, &out)
	}()

	select {
	case stats := <-statsCh:
		if stats.Sent != 2 || stats.Received != 0 {
			t.Fatalf("stats = %+v, want 2 sent / 0 received", stats)
		}
		if stats.Err != nil {
			t.Fatalf("stats.Err = %v, want nil (recv never errored, it just went silent)", stats.Err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runPingLoop did not return within 3s of the last send + grace window; the grace timer likely hung")
	}
}

// TestRunPingLoopSurfacesTransportError covers I6: a genuine transport error
// out of recv() (PermissionDenied, Unauthenticated, mesh-disabled, ...) must
// be captured in stats.Err distinctly from silence, so callers can surface it
// instead of always printing the generic "may need a WendyOS update" hint.
func TestRunPingLoopSurfacesTransportError(t *testing.T) {
	wantErr := errors.New("permission denied: mesh is not enabled")
	session := newFailingPingSession(t.Context(), wantErr)
	var out bytes.Buffer

	statsCh := make(chan pingStats, 1)
	go func() {
		statsCh <- runPingLoop(context.Background(), session, "device-1", 1, 10*time.Millisecond, &out)
	}()

	select {
	case stats := <-statsCh:
		if stats.Sent != 1 || stats.Received != 0 {
			t.Fatalf("stats = %+v, want 1 sent / 0 received", stats)
		}
		if !errors.Is(stats.Err, wantErr) {
			t.Fatalf("stats.Err = %v, want %v", stats.Err, wantErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("runPingLoop did not return within 3s")
	}
}
