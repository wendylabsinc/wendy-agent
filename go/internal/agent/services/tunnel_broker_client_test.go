package services

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

type testAgentTunnelStream struct {
	ctx      context.Context
	received chan *cloudpb.TunnelData
	sent     chan *cloudpb.TunnelData
}

func (s *testAgentTunnelStream) Send(message *cloudpb.TunnelData) error {
	messageCopy := &cloudpb.TunnelData{
		SessionId: message.SessionId,
		Payload:   append([]byte(nil), message.Payload...),
		HalfClose: message.HalfClose,
	}
	select {
	case s.sent <- messageCopy:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *testAgentTunnelStream) Recv() (*cloudpb.TunnelData, error) {
	select {
	case message, ok := <-s.received:
		if !ok {
			return nil, io.EOF
		}
		return message, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *testAgentTunnelStream) CloseSend() error { return nil }

func TestTunnelBrokerRelayDoesNotCancelAfterBackendEOF(t *testing.T) {
	ctx, cancelContext := context.WithCancel(context.Background())
	defer cancelContext()

	var cancelled atomic.Bool
	cancel := func() {
		cancelled.Store(true)
		cancelContext()
	}
	stream := &testAgentTunnelStream{
		ctx:      ctx,
		received: make(chan *cloudpb.TunnelData),
		sent:     make(chan *cloudpb.TunnelData, 4),
	}
	relayConn, backendConn := net.Pipe()
	defer relayConn.Close()

	client := &TunnelBrokerClient{}
	relayDone := make(chan struct{})
	go func() {
		client.relay(ctx, cancel, relayConn, stream)
		close(relayDone)
	}()

	const response = "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"
	go func() {
		_, _ = backendConn.Write([]byte(response))
		_ = backendConn.Close()
	}()

	var payload []byte
	for {
		select {
		case message := <-stream.sent:
			payload = append(payload, message.Payload...)
			if message.HalfClose {
				goto receivedHalfClose
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for agent half-close")
		}
	}

receivedHalfClose:
	if string(payload) != response {
		t.Fatalf("relayed payload = %q, want %q", payload, response)
	}
	time.Sleep(25 * time.Millisecond)
	if cancelled.Load() {
		t.Fatal("relay cancelled the stream before the broker closed its direction")
	}

	close(stream.received)
	select {
	case <-relayDone:
	case <-time.After(time.Second):
		t.Fatal("relay did not finish after both directions closed")
	}
}
