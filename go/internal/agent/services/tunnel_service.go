package services

import (
	"net"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// TunnelService is the device side of `wendy device tunnel`. The destination
// is always agent loopback; callers choose only the TCP port.
type TunnelService struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
	logger    *zap.Logger
	dialLocal func(addr string, timeout time.Duration) (net.Conn, error)
}

func NewTunnelService(logger *zap.Logger) *TunnelService {
	return &TunnelService{
		logger: logger,
		dialLocal: func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("tcp", addr, timeout)
		},
	}
}

func (s *TunnelService) Tunnel(stream agentpbv2.WendyTunnelService_TunnelServer) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "reading tunnel open: %v", err)
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "first tunnel message must be open")
	}
	if open.Port == 0 || open.Port > 65535 {
		return status.Errorf(codes.InvalidArgument, "invalid port %d", open.Port)
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(open.Port)))
	conn, err := s.dialLocal(addr, 10*time.Second)
	if err != nil {
		return status.Errorf(codes.Unavailable, "dialing local port %d: %v", open.Port, err)
	}
	defer conn.Close()

	s.logger.Info("device tunnel accepted",
		append([]zap.Field{zap.Uint32("port", open.Port)}, clientAuditFields(stream.Context())...)...)
	return s.relay(stream, conn)
}

func (s *TunnelService) relay(stream agentpbv2.WendyTunnelService_TunnelServer, conn net.Conn) error {
	errCh := make(chan error, 2)

	go func() {
		<-stream.Context().Done()
		_ = conn.Close()
	}()

	go func() { // gRPC stream -> local TCP
		for {
			msg, err := stream.Recv()
			if err != nil {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
				errCh <- nil
				return
			}
			data := msg.GetData()
			if data == nil {
				continue
			}
			if len(data.Payload) > 0 {
				if _, err := conn.Write(data.Payload); err != nil {
					errCh <- nil
					return
				}
			}
			if data.HalfClose {
				if tc, ok := conn.(*net.TCPConn); ok {
					_ = tc.CloseWrite()
				}
			}
		}
	}()

	go func() { // local TCP -> gRPC stream
		buf := make([]byte, 256*1024)
		for {
			n, readErr := conn.Read(buf)
			if n > 0 {
				payload := append([]byte(nil), buf[:n]...)
				if err := stream.Send(&agentpbv2.DeviceTunnelData{Payload: payload}); err != nil {
					errCh <- nil
					return
				}
			}
			if readErr != nil {
				_ = stream.Send(&agentpbv2.DeviceTunnelData{HalfClose: true})
				errCh <- nil
				return
			}
		}
	}()

	<-errCh
	<-errCh
	return nil
}
