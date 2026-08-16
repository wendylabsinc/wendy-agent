package services

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

type fakeFrameStream struct {
	ctx context.Context
	in  chan tunnelframe.Frame
	out chan tunnelframe.Frame
}

func newFakeFrameStream(ctx context.Context) *fakeFrameStream {
	return &fakeFrameStream{
		ctx: ctx,
		in:  make(chan tunnelframe.Frame, 16),
		out: make(chan tunnelframe.Frame, 16),
	}
}
func (f *fakeFrameStream) Send(fr tunnelframe.Frame) error {
	select {
	case f.out <- fr:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeFrameStream) Recv() (tunnelframe.Frame, error) {
	select {
	case fr, ok := <-f.in:
		if !ok {
			return tunnelframe.Frame{}, io.EOF
		}
		return fr, nil
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}

// startUDPEcho starts a loopback UDP echo server and returns its port.
func startUDPEcho(t *testing.T) uint32 {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDP(buf[:n], addr)
		}
	}()
	return uint32(pc.LocalAddr().(*net.UDPAddr).Port)
}

func awaitFrame(t *testing.T, f *fakeFrameStream) tunnelframe.Frame {
	t.Helper()
	select {
	case fr := <-f.out:
		return fr
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for relay output frame")
		return tunnelframe.Frame{}
	}
}

func TestDatagramRelayUDPRoundTrip(t *testing.T) {
	port := startUDPEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 7, Port: port, Payload: []byte("hello"),
	}}

	reply := awaitFrame(t, stream)
	if reply.Datagram == nil {
		t.Fatalf("expected datagram frame, got %+v", reply)
	}
	if got := reply.Datagram.FlowID; got != 7 {
		t.Fatalf("flow_id = %d, want 7", got)
	}
	if got := string(reply.Datagram.Payload); got != "hello" {
		t.Fatalf("payload = %q, want hello", got)
	}
}

func TestDatagramRelayEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{IcmpRequest: &tunnelframe.IcmpEchoRequest{
		Identifier: 0x1234, Sequence: 3, Payload: []byte("ping"), OriginateUnixNs: 42,
	}}

	reply := awaitFrame(t, stream)
	r := reply.IcmpReply
	if r == nil {
		t.Fatalf("expected icmp_reply frame, got %+v", reply)
	}
	if r.Identifier != 0x1234 || r.Sequence != 3 ||
		string(r.Payload) != "ping" || r.OriginateUnixNs != 42 {
		t.Fatalf("echo fields not copied: %+v", r)
	}
	if r.AgentUnixNs == 0 {
		t.Fatal("agent_unix_ns not stamped")
	}
}

func TestDatagramRelayIdleExpiry(t *testing.T) {
	port := startUDPEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, 50*time.Millisecond)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 1, Port: port, Payload: []byte("x"),
	}}
	awaitFrame(t, stream) // echo comes back → flow exists

	deadline := time.Now().Add(3 * time.Second)
	for relay.activeFlows() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("flow not expired; activeFlows=%d", relay.activeFlows())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDatagramRelayFlowCap verifies flow_id — entirely client-controlled —
// cannot open unbounded sockets/goroutines on the agent: the
// maxFlowsPerSession+1th distinct flow_id is rejected with
// errFlowCapReached (no socket dialed), while re-using an already-open
// flow_id past the cap still succeeds (it's a write, not a new fd).
func TestDatagramRelayFlowCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)

	// UDP connect() doesn't require a listener on the far end, so distinct
	// loopback ports below are fine to dial without an echo server.
	for i := uint32(0); i < maxFlowsPerSession; i++ {
		if _, err := relay.flow(ctx, i+1, 9); err != nil {
			t.Fatalf("flow %d: unexpected error opening flow within cap: %v", i+1, err)
		}
	}
	if got := relay.activeFlows(); got != maxFlowsPerSession {
		t.Fatalf("activeFlows = %d, want %d", got, maxFlowsPerSession)
	}

	if _, err := relay.flow(ctx, maxFlowsPerSession+100, 9); !errors.Is(err, errFlowCapReached) {
		t.Fatalf("flow beyond cap: err = %v, want errFlowCapReached", err)
	}
	if got := relay.activeFlows(); got != maxFlowsPerSession {
		t.Fatalf("activeFlows after rejected flow = %d, want unchanged %d", got, maxFlowsPerSession)
	}

	// An already-open flow_id is a write to an existing socket, not a new
	// fd, so it must succeed even though the session is at the cap.
	if _, err := relay.flow(ctx, 1, 9); err != nil {
		t.Fatalf("re-using an already-open flow_id at cap: unexpected error: %v", err)
	}
}

func TestDatagramRelayDropsOversized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 1, Port: 9, Payload: make([]byte, maxUDPPayload+1),
	}}
	// Follow with a valid echo; if the oversized frame had opened a flow or
	// crashed the loop, this would not come back.
	stream.in <- tunnelframe.Frame{IcmpRequest: &tunnelframe.IcmpEchoRequest{Identifier: 1, Sequence: 1}}
	reply := awaitFrame(t, stream)
	if reply.IcmpReply == nil {
		t.Fatalf("relay loop broken after oversized datagram: %+v", reply)
	}
	if relay.activeFlows() != 0 {
		t.Fatalf("oversized datagram opened a flow")
	}
}
