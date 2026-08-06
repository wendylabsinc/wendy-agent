package commands

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// echoDatagramTunnelServer echoes every datagram frame straight back,
// standing in for a real agent's DatagramTunnel handler.
type echoDatagramTunnelServer struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
}

func (s *echoDatagramTunnelServer) DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

func dialFakeAgent(t *testing.T, server agentpbv2.WendyTunnelServiceServer) agentpbv2.WendyTunnelServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	agentpbv2.RegisterWendyTunnelServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return agentpbv2.NewWendyTunnelServiceClient(conn)
}

func TestDeviceDatagramSessionUDPRoundTrip(t *testing.T) {
	client := dialFakeAgent(t, &echoDatagramTunnelServer{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := openDeviceDatagramSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	if err := session.sendDatagram(4, 9000, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	f, err := session.recv()
	if err != nil {
		t.Fatal(err)
	}
	if f.Datagram == nil || f.Datagram.FlowID != 4 || string(f.Datagram.Payload) != "hello" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}
