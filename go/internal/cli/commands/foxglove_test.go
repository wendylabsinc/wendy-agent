package commands

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// --- fake stream ---

// fakeRawStream satisfies grpc.ServerStreamingClient[agentpbv2.RawROS2Message].
// It embeds grpc.ClientStream to cover the non-Recv methods; only Recv is used
// by the server, so nil-method panics from the embedded interface are fine.
type fakeRawStream struct {
	grpc.ClientStream // nil embedded interface — panics if non-Recv methods called
	msgs              []*agentpbv2.RawROS2Message
	i                 int
	ctx               context.Context
}

func (f *fakeRawStream) Recv() (*agentpbv2.RawROS2Message, error) {
	if f.i >= len(f.msgs) {
		<-f.ctx.Done() // block until cancelled, like a live stream awaiting more
		return nil, io.EOF
	}
	m := f.msgs[f.i]
	f.i++
	return m, nil
}

// Ensure fakeRawStream satisfies the generic streaming client interface.
var _ grpc.ServerStreamingClient[agentpbv2.RawROS2Message] = (*fakeRawStream)(nil)

// --- fake source ---

type fakeFGSource struct{ ctx context.Context }

func (s fakeFGSource) ListTopics(_ context.Context, _ *agentpbv2.ListROS2TopicsRequest, _ ...grpc.CallOption) (*agentpbv2.ListROS2TopicsResponse, error) {
	return &agentpbv2.ListROS2TopicsResponse{
		Topics: []*agentpbv2.ROS2Topic{{Name: "/chatter"}},
	}, nil
}

func (s fakeFGSource) GetMessageDefinition(_ context.Context, _ *agentpbv2.GetROS2MessageDefinitionRequest, _ ...grpc.CallOption) (*agentpbv2.GetROS2MessageDefinitionResponse, error) {
	return &agentpbv2.GetROS2MessageDefinitionResponse{
		MessageType: "std_msgs/msg/String",
		Schema:      "string data",
	}, nil
}

func (s fakeFGSource) SubscribeRaw(ctx context.Context, _ *agentpbv2.SubscribeRawROS2Request, _ ...grpc.CallOption) (grpc.ServerStreamingClient[agentpbv2.RawROS2Message], error) {
	return &fakeRawStream{
		ctx:  ctx,
		msgs: []*agentpbv2.RawROS2Message{{Cdr: []byte{0xDE, 0xAD}, TimestampNs: 42}},
	}, nil
}

// Compile-time assertion: fakeFGSource must satisfy foxgloveSource.
var _ foxgloveSource = fakeFGSource{}

// eofRawStream yields its messages then returns io.EOF (it does not block like
// fakeRawStream), so a pump consuming it terminates on its own.
type eofRawStream struct {
	grpc.ClientStream
	msgs []*agentpbv2.RawROS2Message
	i    int
}

func (f *eofRawStream) Recv() (*agentpbv2.RawROS2Message, error) {
	if f.i >= len(f.msgs) {
		return nil, io.EOF
	}
	m := f.msgs[f.i]
	f.i++
	return m, nil
}

// eofFGSource hands pump an eofRawStream.
type eofFGSource struct{ stream *eofRawStream }

func (s eofFGSource) ListTopics(context.Context, *agentpbv2.ListROS2TopicsRequest, ...grpc.CallOption) (*agentpbv2.ListROS2TopicsResponse, error) {
	return &agentpbv2.ListROS2TopicsResponse{}, nil
}
func (s eofFGSource) GetMessageDefinition(context.Context, *agentpbv2.GetROS2MessageDefinitionRequest, ...grpc.CallOption) (*agentpbv2.GetROS2MessageDefinitionResponse, error) {
	return &agentpbv2.GetROS2MessageDefinitionResponse{}, nil
}
func (s eofFGSource) SubscribeRaw(context.Context, *agentpbv2.SubscribeRawROS2Request, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpbv2.RawROS2Message], error) {
	return s.stream, nil
}

// TestPump_DropsWhenClientSlow verifies pump never blocks on a write queue the
// client isn't draining: it drops frames and keeps making progress (Finding 6 —
// slow-client memory growth / DoS). Before the drop policy, pump blocked forever
// on `out <- frame` and this test would time out.
func TestPump_DropsWhenClientSlow(t *testing.T) {
	const n = 200
	msgs := make([]*agentpbv2.RawROS2Message, n)
	for i := range msgs {
		msgs[i] = &agentpbv2.RawROS2Message{Cdr: []byte{byte(i)}, TimestampNs: 1}
	}
	srv := &foxgloveServer{src: eofFGSource{stream: &eofRawStream{msgs: msgs}}}

	out := make(chan []byte, 1) // tiny and never drained -> forces drops
	var dropped atomic.Uint64
	done := make(chan struct{})
	go func() {
		srv.pump(context.Background(), 7, "/t", out, &dropped)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not return — it blocked on the full write queue instead of dropping")
	}
	if dropped.Load() == 0 {
		t.Fatal("expected dropped > 0 with an undrained write queue, got 0")
	}
}

// --- test server helper ---

func startTestServer(t *testing.T, src foxgloveSource) (string, func()) {
	t.Helper()
	srv := &foxgloveServer{src: src}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{fgSubprotocol}})
		if err != nil {
			return
		}
		srv.handleConn(r.Context(), c)
	}))
	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	return url, ts.Close
}

// --- tests ---

func TestFoxgloveServer_Handshake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url, closeFn := startTestServer(t, fakeFGSource{ctx: ctx})
	defer closeFn()

	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{fgSubprotocol}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	// 1. serverInfo
	_, info, rerr := c.Read(ctx)
	if rerr != nil {
		t.Fatalf("read serverInfo: %v", rerr)
	}
	var si map[string]any
	if err := json.Unmarshal(info, &si); err != nil {
		t.Fatalf("unmarshal serverInfo: %v", err)
	}
	if si["op"] != "serverInfo" {
		t.Fatalf("first msg op = %v, want serverInfo", si["op"])
	}

	// 2. advertise
	_, advRaw, rerr := c.Read(ctx)
	if rerr != nil {
		t.Fatalf("read advertise: %v", rerr)
	}
	var adv fgAdvertise
	if err := json.Unmarshal(advRaw, &adv); err != nil {
		t.Fatalf("unmarshal advertise: %v", err)
	}
	if len(adv.Channels) != 1 || adv.Channels[0].Topic != "/chatter" {
		t.Fatalf("advertise = %+v", adv)
	}
	ch := adv.Channels[0]

	// 3. subscribe to that channel
	sub, err := json.Marshal(map[string]any{
		"op": "subscribe",
		"subscriptions": []map[string]any{
			{"id": 99, "channelId": ch.ID},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, websocket.MessageText, sub); err != nil {
		t.Fatalf("write subscribe: %v", err)
	}

	// 4. expect one binary MESSAGE_DATA frame for sub id 99 with payload 0xDEAD
	typ, frame, rerr := c.Read(ctx)
	if rerr != nil {
		t.Fatalf("read message-data: %v", rerr)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("expected binary message, got %v", typ)
	}
	// frame layout: [op(1)] [subID(4 LE)] [timestamp(8 LE)] [payload...]
	if frame[0] != 0x01 {
		t.Fatalf("expected MESSAGE_DATA op=0x01, got op=%d", frame[0])
	}
	if binary.LittleEndian.Uint32(frame[1:5]) != 99 {
		t.Fatalf("subID = %d, want 99", binary.LittleEndian.Uint32(frame[1:5]))
	}
	if string(frame[13:]) != string([]byte{0xDE, 0xAD}) {
		t.Fatalf("payload = % x, want DE AD", frame[13:])
	}
}
