package commands

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// fakePingSession answers every echo request immediately. It selects on ctx
// everywhere it can block so a cancelled test context always unblocks both
// sendEcho and recv, instead of leaking a goroutine parked on an idle channel
// past test end (see fakeDatagramSession in cloud_datagram_test.go and
// fakeAgentStream in go/internal/agent/services/datagram_relay_test.go for
// the same pattern).
type fakePingSession struct {
	ctx     context.Context
	mu      sync.Mutex
	replies chan *cloudpb.TunnelData
	drop    bool // when true, swallow requests (packet loss)
}

func newFakePingSession(ctx context.Context) *fakePingSession {
	return &fakePingSession{ctx: ctx, replies: make(chan *cloudpb.TunnelData, 16)}
}
func (f *fakePingSession) sendEcho(req *cloudpb.IcmpEchoRequest) error {
	f.mu.Lock()
	drop := f.drop
	f.mu.Unlock()
	if drop {
		return nil
	}
	reply := &cloudpb.TunnelData{IcmpReply: &cloudpb.IcmpEchoReply{
		Identifier:      req.GetIdentifier(),
		Sequence:        req.GetSequence(),
		Payload:         req.GetPayload(),
		OriginateUnixNs: req.GetOriginateUnixNs(),
		AgentUnixNs:     uint64(time.Now().UnixNano()),
	}}
	select {
	case f.replies <- reply:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakePingSession) recv() (*cloudpb.TunnelData, error) {
	select {
	case d, ok := <-f.replies:
		if !ok {
			return nil, io.EOF
		}
		return d, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
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
	case <-time.After(3 * time.Second):
		t.Fatal("runPingLoop did not return within 3s of the last send + grace window; the grace timer likely hung")
	}
}
