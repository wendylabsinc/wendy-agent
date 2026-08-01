package services

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type fakeDeviceTunnelStream struct {
	agentpbv2.WendyTunnelService_TunnelServer
	ctx   context.Context
	input []*agentpbv2.DeviceTunnelRequest
}

func (f *fakeDeviceTunnelStream) Context() context.Context { return f.ctx }

func (f *fakeDeviceTunnelStream) Recv() (*agentpbv2.DeviceTunnelRequest, error) {
	if len(f.input) == 0 {
		return nil, errors.New("end of test input")
	}
	msg := f.input[0]
	f.input = f.input[1:]
	return msg, nil
}

func TestTunnelServiceRequiresOpenFirst(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	stream := &fakeDeviceTunnelStream{
		ctx: context.Background(),
		input: []*agentpbv2.DeviceTunnelRequest{{
			Content: &agentpbv2.DeviceTunnelRequest_Data{Data: &agentpbv2.DeviceTunnelData{Payload: []byte("x")}},
		}},
	}
	if got := status.Code(svc.Tunnel(stream)); got != codes.InvalidArgument {
		t.Fatalf("status = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestTunnelServiceRejectsInvalidPort(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	stream := &fakeDeviceTunnelStream{
		ctx: context.Background(),
		input: []*agentpbv2.DeviceTunnelRequest{{
			Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{}},
		}},
	}
	if got := status.Code(svc.Tunnel(stream)); got != codes.InvalidArgument {
		t.Fatalf("status = %v, want %v", got, codes.InvalidArgument)
	}
}

func TestTunnelServiceDialsOnlyLoopback(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	var gotAddr string
	svc.dialLocal = func(addr string, _ time.Duration) (net.Conn, error) {
		gotAddr = addr
		return nil, errors.New("stop after capturing address")
	}
	stream := &fakeDeviceTunnelStream{
		ctx: context.Background(),
		input: []*agentpbv2.DeviceTunnelRequest{{
			Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: 8765}},
		}},
	}
	if got := status.Code(svc.Tunnel(stream)); got != codes.Unavailable {
		t.Fatalf("status = %v, want %v", got, codes.Unavailable)
	}
	if gotAddr != "127.0.0.1:8765" {
		t.Fatalf("dial address = %q, want loopback", gotAddr)
	}
}

func TestTunnelServiceRelaysTCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcpListener.Close() })
	go func() {
		conn, acceptErr := tcpListener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	grpcListener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	agentpbv2.RegisterWendyTunnelServiceServer(server, NewTunnelService(zap.NewNop()))
	go func() { _ = server.Serve(grpcListener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = grpcListener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	grpcConn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return grpcListener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer grpcConn.Close()

	stream, err := agentpbv2.NewWendyTunnelServiceClient(grpcConn).Tunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port := uint32(tcpListener.Addr().(*net.TCPAddr).Port)
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{
		Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: port}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{
		Content: &agentpbv2.DeviceTunnelRequest_Data{Data: &agentpbv2.DeviceTunnelData{Payload: []byte("foxglove")}},
	}); err != nil {
		t.Fatal(err)
	}
	response, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if string(response.GetPayload()) != "foxglove" {
		t.Fatalf("response = %q, want foxglove", response.GetPayload())
	}
	_ = stream.CloseSend()
}
