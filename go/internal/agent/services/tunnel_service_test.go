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

type fakeDeviceDatagramStream struct {
	agentpbv2.WendyTunnelService_DatagramTunnelServer
	ctx context.Context
	in  chan *agentpbv2.DeviceDatagramFrame
	out chan *agentpbv2.DeviceDatagramFrame
}

func newFakeDeviceDatagramStream(ctx context.Context) *fakeDeviceDatagramStream {
	return &fakeDeviceDatagramStream{
		ctx: ctx,
		in:  make(chan *agentpbv2.DeviceDatagramFrame, 16),
		out: make(chan *agentpbv2.DeviceDatagramFrame, 16),
	}
}
func (f *fakeDeviceDatagramStream) Context() context.Context { return f.ctx }
func (f *fakeDeviceDatagramStream) Send(msg *agentpbv2.DeviceDatagramFrame) error {
	select {
	case f.out <- msg:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeDeviceDatagramStream) Recv() (*agentpbv2.DeviceDatagramFrame, error) {
	select {
	case msg, ok := <-f.in:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func TestTunnelServiceDatagramTunnelEchoesICMP(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeDeviceDatagramStream(ctx)
	svc := NewTunnelService(zap.NewNop())

	go func() { _ = svc.DatagramTunnel(stream) }()

	stream.in <- &agentpbv2.DeviceDatagramFrame{
		Content: &agentpbv2.DeviceDatagramFrame_IcmpRequest{
			IcmpRequest: &agentpbv2.DeviceIcmpEchoRequest{
				Identifier: 9, Sequence: 1, Payload: []byte("ping"), OriginateUnixNs: 42,
			},
		},
	}

	select {
	case reply := <-stream.out:
		r := reply.GetIcmpReply()
		if r == nil {
			t.Fatalf("expected icmp_reply, got %+v", reply)
		}
		if r.GetIdentifier() != 9 || r.GetSequence() != 1 || string(r.GetPayload()) != "ping" {
			t.Fatalf("echo fields not copied: %+v", r)
		}
		if r.GetAgentUnixNs() == 0 {
			t.Fatal("agent_unix_ns not stamped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for icmp reply")
	}
}

func TestTunnelServiceDatagramTunnelUDPRoundTrip(t *testing.T) {
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
	port := uint32(pc.LocalAddr().(*net.UDPAddr).Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeDeviceDatagramStream(ctx)
	svc := NewTunnelService(zap.NewNop())

	go func() { _ = svc.DatagramTunnel(stream) }()

	stream.in <- &agentpbv2.DeviceDatagramFrame{
		Content: &agentpbv2.DeviceDatagramFrame_Datagram{
			Datagram: &agentpbv2.DeviceDatagram{FlowId: 3, Port: port, Payload: []byte("hello")},
		},
	}

	select {
	case reply := <-stream.out:
		d := reply.GetDatagram()
		if d == nil || d.GetFlowId() != 3 || string(d.GetPayload()) != "hello" {
			t.Fatalf("unexpected datagram reply: %+v", reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for udp echo")
	}
}
