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

func TestRunPingLoopCountsLoss(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	session := newFakePingSession(ctx)
	session.drop = true
	var out bytes.Buffer
	stats := runPingLoop(ctx, session, "device-1", 2, 10*time.Millisecond, &out)
	if stats.Sent != 2 || stats.Received != 0 {
		t.Fatalf("stats = %+v, want 2 sent / 0 received", stats)
	}
}
