package commands

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type echoDeviceTunnelServer struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
	opened chan uint32
}

func (s *echoDeviceTunnelServer) Tunnel(stream agentpbv2.WendyTunnelService_TunnelServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	s.opened <- first.GetOpen().GetPort()
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		data := msg.GetData()
		if len(data.GetPayload()) > 0 {
			if err := stream.Send(&agentpbv2.DeviceTunnelData{Payload: data.GetPayload()}); err != nil {
				return err
			}
		}
		if data.GetHalfClose() {
			return stream.Send(&agentpbv2.DeviceTunnelData{HalfClose: true})
		}
	}
}

func TestOpenDeviceTunnelRelaysBytes(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	echo := &echoDeviceTunnelServer{opened: make(chan uint32, 1)}
	agentpbv2.RegisterWendyTunnelServiceServer(server, echo)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	tunnel, err := openDeviceTunnel(ctx, agentpbv2.NewWendyTunnelServiceClient(conn), 8765)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	if _, err := tunnel.Write([]byte("foxglove")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("foxglove"))
	if _, err := io.ReadFull(tunnel, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "foxglove" {
		t.Fatalf("echo = %q, want foxglove", got)
	}
	select {
	case port := <-echo.opened:
		if port != 8765 {
			t.Fatalf("remote port = %d, want 8765", port)
		}
	case <-ctx.Done():
		t.Fatal("server did not receive tunnel open")
	}
}
