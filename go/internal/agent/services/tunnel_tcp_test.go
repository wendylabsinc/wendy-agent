package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// Use real gRPC streams so returning a handler cancels blocked Recv/Send calls,
// just as it does on the mTLS listener. These tests exercise framing and TCP;
// certificate/tenant admission remains the server interceptor's responsibility.
func tcpTunnelClient(t *testing.T, svc *TunnelService) (agentpbv2.WendyTunnelServiceClient, <-chan error) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	completed := make(chan error, 32)
	server := grpc.NewServer(grpc.StreamInterceptor(func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		err := handler(srv, stream)
		completed <- err
		return err
	}))
	agentpbv2.RegisterWendyTunnelServiceServer(server, svc)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///tunnel", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentpbv2.NewWendyTunnelServiceClient(conn), completed
}

func tcpTunnelContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func tcpTunnelListener(t *testing.T, host string) (*net.TCPListener, uint32) {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP(host)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener, uint32(listener.Addr().(*net.TCPAddr).Port)
}

func tcpTunnelOpen(t *testing.T, client agentpbv2.WendyTunnelServiceClient, ctx context.Context, port uint32) agentpbv2.WendyTunnelService_TunnelClient {
	t.Helper()
	stream, err := client.Tunnel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: port}}}); err != nil {
		t.Fatal(err)
	}
	return stream
}

func tcpTunnelSend(t *testing.T, stream agentpbv2.WendyTunnelService_TunnelClient, payload []byte, halfClose bool) {
	t.Helper()
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Data{Data: &agentpbv2.DeviceTunnelData{Payload: payload, HalfClose: halfClose}}}); err != nil {
		t.Fatal(err)
	}
}

func tcpTunnelResult(t *testing.T, completed <-chan error) error {
	t.Helper()
	select {
	case err := <-completed:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
		return nil
	}
}

func TestTCPTunnelHalfCloseDrainsResponse(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(strconv.FormatBool(explicit), func(t *testing.T) {
			svc := NewTunnelService(zap.NewNop())
			client, completed := tcpTunnelClient(t, svc)
			listener, port := tcpTunnelListener(t, "127.0.0.1")
			// More than a relay buffer in each direction, and payload on half-close.
			request := bytes.Repeat([]byte("request"), 20000)
			response := bytes.Repeat([]byte("response"), 20000)
			peerDone := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptTCP()
				if err != nil {
					peerDone <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				got, err := io.ReadAll(conn)
				if err == nil && !bytes.Equal(got, request) {
					err = errors.New("request differs")
				}
				if err == nil {
					_, err = conn.Write(response)
				}
				peerDone <- err
			}()
			stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), port)
			tcpTunnelSend(t, stream, request, explicit)
			if !explicit {
				if err := stream.CloseSend(); err != nil {
					t.Fatal(err)
				}
			}
			var got []byte
			halfClosed := false
			for {
				frame, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, frame.Payload...)
				halfClosed = halfClosed || frame.HalfClose
			}
			if !bytes.Equal(got, response) || !halfClosed {
				t.Fatalf("response bytes = %d, half-close = %v", len(got), halfClosed)
			}
			if err := tcpTunnelResult(t, peerDone); err != nil {
				t.Fatal(err)
			}
			if err := tcpTunnelResult(t, completed); err != nil {
				t.Fatal(err)
			}
			if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
				t.Fatal("slots not released")
			}
		})
	}
}

func TestTCPTunnelServerHalfCloseKeepsUploadOpen(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	client, completed := tcpTunnelClient(t, svc)
	listener, port := tcpTunnelListener(t, "127.0.0.1")
	peerDone := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err != nil {
			peerDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		_, err = conn.Write([]byte("greeting"))
		if err == nil {
			err = conn.CloseWrite()
		}
		if err == nil {
			var payload []byte
			payload, err = io.ReadAll(conn)
			if err == nil && string(payload) != "after EOF" {
				err = errors.New("upload lost after server half-close")
			}
		}
		peerDone <- err
	}()
	stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), port)
	var got []byte
	for {
		frame, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, frame.Payload...)
		if frame.HalfClose {
			break
		}
	}
	if string(got) != "greeting" {
		t.Fatalf("greeting = %q", got)
	}
	tcpTunnelSend(t, stream, []byte("after EOF"), true)
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("final receive = %v", err)
	}
	if err := tcpTunnelResult(t, completed); err != nil {
		t.Fatal(err)
	}
	if err := tcpTunnelResult(t, peerDone); err != nil {
		t.Fatal(err)
	}
}

func TestTCPTunnelRejectsInvalidOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first *agentpbv2.DeviceTunnelRequest
	}{
		{name: "empty stream"},
		{name: "empty frame", first: &agentpbv2.DeviceTunnelRequest{}},
		{name: "data first", first: &agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Data{Data: &agentpbv2.DeviceTunnelData{Payload: []byte("no")}}}},
		{name: "zero port", first: &agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{}}}},
		{name: "overflow port", first: &agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: 65536}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewTunnelService(zap.NewNop())
			svc.dialTCP = func(context.Context, string, string) (net.Conn, error) {
				t.Error("invalid request dialed")
				return nil, errors.New("unexpected dial")
			}
			client, completed := tcpTunnelClient(t, svc)
			stream, err := client.Tunnel(tcpTunnelContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if tc.first != nil {
				_ = stream.Send(tc.first)
			}
			_ = stream.CloseSend()
			if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status = %v", err)
			}
			_ = tcpTunnelResult(t, completed)
			if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
				t.Fatal("slots leaked")
			}
		})
	}
}

func TestTCPTunnelAlwaysDialsLoopback(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	target := make(chan string, 1)
	svc.dialTCP = func(_ context.Context, network, address string) (net.Conn, error) {
		target <- network + " " + address
		return nil, errors.New("private dial diagnostic")
	}
	client, completed := tcpTunnelClient(t, svc)
	stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), 8080)
	if _, err := stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("status = %v", err)
	} else if strings.Contains(status.Convert(err).Message(), "private dial diagnostic") {
		t.Fatalf("dial diagnostic exposed to client: %v", err)
	}
	_ = tcpTunnelResult(t, completed)
	if got := <-target; got != "tcp4 127.0.0.1:8080" {
		t.Fatalf("dial = %q", got)
	}
	if len(svc.flowSlots) != 0 {
		t.Fatal("failed dial leaked socket slot")
	}
}

func TestTCPTunnelCannotReachNonLoopbackListener(t *testing.T) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		ipnet, ok := address.(*net.IPNet)
		if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
			continue
		}
		listener, port := tcpTunnelListener(t, ipnet.IP.String())
		// Keeping the non-loopback listener alive ensures its port is a real
		// target. The service must refuse rather than reaching it via this IP.
		_ = listener
		svc := NewTunnelService(zap.NewNop())
		client, completed := tcpTunnelClient(t, svc)
		stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), port)
		if _, err := stream.Recv(); status.Code(err) != codes.Unavailable {
			t.Fatalf("non-loopback open = %v", err)
		}
		_ = tcpTunnelResult(t, completed)
		return
	}
	t.Skip("no non-loopback IPv4 interface")
}

func TestTCPTunnelCapsPendingStreamsAndSharedSockets(t *testing.T) {
	for _, socketCap := range []bool{false, true} {
		t.Run(strconv.FormatBool(socketCap), func(t *testing.T) {
			svc := NewTunnelService(zap.NewNop())
			svc.tcpSessions = make(chan struct{}, 1)
			if socketCap {
				svc.flowSlots = make(chan struct{}, 1)
				svc.flowSlots <- struct{}{}
			}
			client, completed := tcpTunnelClient(t, svc)
			ctx, cancel := context.WithCancel(tcpTunnelContext(t))
			defer cancel()
			stream, err := client.Tunnel(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if socketCap {
				_ = stream.Send(&agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: 8080}}})
			} else {
				deadline := time.Now().Add(5 * time.Second)
				for len(svc.tcpSessions) != 1 {
					if time.Now().After(deadline) {
						t.Fatal("pending stream not counted")
					}
					time.Sleep(time.Millisecond)
				}
				stream, err = client.Tunnel(tcpTunnelContext(t))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := stream.Recv(); status.Code(err) != codes.ResourceExhausted {
				t.Fatalf("cap status = %v", err)
			}
			_ = tcpTunnelResult(t, completed)
			cancel()
			if !socketCap {
				_ = tcpTunnelResult(t, completed)
			} else {
				<-svc.flowSlots
			}
			// A rejected stream or a cancelled pending open must free its slot.
			retry, err := client.Tunnel(tcpTunnelContext(t))
			if err != nil {
				t.Fatal(err)
			}
			_ = retry.CloseSend()
			if _, err := retry.Recv(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("slot reuse = %v", err)
			}
			_ = tcpTunnelResult(t, completed)
			if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
				t.Fatal("slots leaked")
			}
		})
	}
}

func TestTCPTunnelCancellationClosesSocket(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	client, completed := tcpTunnelClient(t, svc)
	listener, port := tcpTunnelListener(t, "127.0.0.1")
	accepted := make(chan *net.TCPConn, 1)
	go func() { conn, _ := listener.AcceptTCP(); accepted <- conn }()
	ctx, cancel := context.WithCancel(tcpTunnelContext(t))
	defer cancel()
	stream := tcpTunnelOpen(t, client, ctx, port)
	var conn *net.TCPConn
	select {
	case conn = <-accepted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if conn == nil {
		t.Fatal("accept failed")
	}
	defer conn.Close()
	cancel()
	if _, err := stream.Recv(); status.Code(err) != codes.Canceled {
		t.Fatalf("cancel status = %v", err)
	}
	_ = tcpTunnelResult(t, completed)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("socket remained open: %v", err)
	}
	if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
		t.Fatal("slots leaked")
	}
}

func TestTCPTunnelCancellationInterruptsDial(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	dialing := make(chan struct{})
	svc.dialTCP = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	client, completed := tcpTunnelClient(t, svc)
	ctx, cancel := context.WithCancel(tcpTunnelContext(t))
	defer cancel()
	_ = tcpTunnelOpen(t, client, ctx, 8080)
	select {
	case <-dialing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	if err := tcpTunnelResult(t, completed); status.Code(err) != codes.Canceled {
		t.Fatalf("dial cancel = %v", err)
	}
	if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
		t.Fatal("slots leaked")
	}
}

func TestTCPTunnelRejectsRepeatedOpen(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	client, completed := tcpTunnelClient(t, svc)
	listener, port := tcpTunnelListener(t, "127.0.0.1")
	stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), port)
	if err := stream.Send(&agentpbv2.DeviceTunnelRequest{Content: &agentpbv2.DeviceTunnelRequest_Open{Open: &agentpbv2.DeviceTunnelOpen{Port: port}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("repeated open = %v", err)
	}
	_ = tcpTunnelResult(t, completed)
	conn, err := listener.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("socket remained open: %v", err)
	}
}

func TestTCPTunnelResetReleasesPendingReceive(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	client, completed := tcpTunnelClient(t, svc)
	listener, port := tcpTunnelListener(t, "127.0.0.1")
	reset := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptTCP()
		if err == nil {
			err = conn.SetLinger(0)
			_ = conn.Close()
		}
		reset <- err
	}()
	stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), port)
	// Keep the sending side open; a reset must finish without waiting on Recv.
	if _, err := stream.Recv(); status.Code(err) != codes.Unavailable {
		t.Fatalf("reset = %v", err)
	}
	_ = tcpTunnelResult(t, completed)
	if err := tcpTunnelResult(t, reset); err != nil {
		t.Fatal(err)
	}
	if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
		t.Fatal("slots leaked")
	}
}

func TestTCPTunnelUnsupportedHalfCloseFailsWithoutPanic(t *testing.T) {
	svc := NewTunnelService(zap.NewNop())
	conn, peer := net.Pipe()
	defer peer.Close()
	svc.dialTCP = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
	client, completed := tcpTunnelClient(t, svc)
	stream := tcpTunnelOpen(t, client, tcpTunnelContext(t), 8080)
	if _, err := stream.Recv(); status.Code(err) != codes.Internal {
		t.Fatalf("unsupported half-close = %v", err)
	}
	_ = tcpTunnelResult(t, completed)
	if len(svc.tcpSessions) != 0 || len(svc.flowSlots) != 0 {
		t.Fatal("slots leaked")
	}
}
