package services

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// Sessions may be long-lived because an active Companion port forward is
// expected to run until explicitly stopped. Bound both dimensions instead:
// idle UDP sockets expire after datagramFlowIdleTimeout, each datagram session
// gets the relay's 256-flow cap, and TCP and UDP share the socket budget below.
const (
	maxConcurrentDatagramSessions = 8
	maxConcurrentDatagramFlows    = 512
	// Each TCP stream owns one socket. Count pending opens too, and share
	// the socket budget with UDP so the transports cannot exhaust it separately.
	maxConcurrentTCPTunnels = 256
)

// TunnelService exposes authenticated loopback TCP and datagram sessions.
// It is registered only on the authenticated, tenant-checked mTLS server.
type TunnelService struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
	logger      *zap.Logger
	sessions    chan struct{}
	flowSlots   chan struct{}
	tcpSessions chan struct{}
	dialTCP     func(context.Context, string, string) (net.Conn, error)
}

func NewTunnelService(logger *zap.Logger) *TunnelService {
	return &TunnelService{
		logger:      logger,
		sessions:    make(chan struct{}, maxConcurrentDatagramSessions),
		flowSlots:   make(chan struct{}, maxConcurrentDatagramFlows),
		tcpSessions: make(chan struct{}, maxConcurrentTCPTunnels),
		dialTCP:     (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
}

// Tunnel maps one stream to one TCP connection. The wire contract accepts only
// a port: using a literal loopback IP also avoids DNS or caller-selected hosts.
func (s *TunnelService) Tunnel(stream agentpbv2.WendyTunnelService_TunnelServer) error {
	select {
	case s.tcpSessions <- struct{}{}:
		defer func() { <-s.tcpSessions }()
	default:
		return status.Error(codes.ResourceExhausted, "too many active tunnel sessions")
	}

	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "first tunnel message must be open")
		}
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return status.Error(codes.InvalidArgument, "first tunnel message must be open")
	}
	if open.Port == 0 || open.Port > 65535 {
		return status.Errorf(codes.InvalidArgument, "invalid port %d", open.Port)
	}
	select {
	case s.flowSlots <- struct{}{}:
		defer func() { <-s.flowSlots }()
	default:
		return status.Error(codes.ResourceExhausted, "too many active tunnel flows")
	}

	conn, err := s.dialTCP(stream.Context(), "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(open.Port))))
	if err != nil {
		if stream.Context().Err() != nil {
			return status.FromContextError(stream.Context().Err()).Err()
		}
		return status.Errorf(codes.Unavailable, "dialing local port %d: %v", open.Port, err)
	}
	defer conn.Close()
	s.logger.Info("device tunnel accepted",
		append([]zap.Field{zap.Uint32("port", open.Port)}, clientAuditFields(stream.Context())...)...)
	return relayDeviceTCP(stream, conn)
}

func relayDeviceTCP(stream agentpbv2.WendyTunnelService_TunnelServer, conn net.Conn) error {
	// A hard failure returns immediately: gRPC cancels any parked Recv/Send
	// when the handler returns. Graceful half-closes wait for both directions
	// so an EOF-delimited request can still receive its complete response.
	done := make(chan error, 2)
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil && !errors.Is(err, io.EOF) {
				done <- err
				return
			}
			var data *agentpbv2.DeviceTunnelData
			if err == nil {
				data = msg.GetData()
				if data == nil {
					done <- status.Error(codes.InvalidArgument, "expected data frame")
					return
				}
				if _, err := conn.Write(data.Payload); err != nil {
					done <- status.Error(codes.Unavailable, "writing tunnel data")
					return
				}
			}
			if errors.Is(err, io.EOF) || data.GetHalfClose() {
				err := conn.(interface{ CloseWrite() error }).CloseWrite()
				if err != nil {
					err = status.Error(codes.Unavailable, "closing tunnel write side")
				}
				done <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&agentpbv2.DeviceTunnelData{Payload: append([]byte(nil), buf[:n]...)}); sendErr != nil {
					done <- sendErr
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					done <- stream.Send(&agentpbv2.DeviceTunnelData{HalfClose: true})
				} else {
					done <- status.Error(codes.Unavailable, "reading tunnel data")
				}
				return
			}
		}
	}()
	for range 2 {
		select {
		case <-stream.Context().Done():
			return status.FromContextError(stream.Context().Err()).Err()
		case err := <-done:
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// DatagramTunnel serves one multiplexed UDP + ICMP-echo session for a LAN
// client. The existing datagram relay keeps every UDP destination on agent
// loopback and treats echo frames as application-level replies; it does not
// open a raw ICMP socket.
func (s *TunnelService) DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error {
	select {
	case s.sessions <- struct{}{}:
		defer func() { <-s.sessions }()
	default:
		return status.Error(codes.ResourceExhausted, "too many active datagram tunnel sessions")
	}

	// SECURITY: The established audit helper records the remote address,
	// certificate serial, and parsed org/entity IDs when available. It never
	// records certificate subjects, free-form caller input, or frame payloads.
	s.logger.Info("device datagram tunnel accepted", clientAuditFields(stream.Context())...)
	newDatagramRelay(s.logger, &deviceFrameStream{stream: stream}, datagramFlowIdleTimeout,
		withDatagramFlowSlots(s.flowSlots)).run(stream.Context())
	return nil
}

// deviceFrameStream adapts the LAN service's wire messages to the Cloud frame
// types used internally by the existing datagram relay. It keeps the current
// Cloud path untouched while both transports share the same bounded relay.
type deviceFrameStream struct {
	stream agentpbv2.WendyTunnelService_DatagramTunnelServer
}

func (d *deviceFrameStream) Send(msg *cloudpbv2.TunnelData) error {
	frame := &agentpbv2.DeviceDatagramFrame{}
	switch {
	case msg.GetDatagram() != nil:
		datagram := msg.GetDatagram()
		frame.Content = &agentpbv2.DeviceDatagramFrame_Datagram{Datagram: &agentpbv2.DeviceDatagram{
			FlowId: datagram.GetFlowId(), Port: datagram.GetPort(), Payload: datagram.GetPayload(),
		}}
	case msg.GetIcmpRequest() != nil:
		request := msg.GetIcmpRequest()
		frame.Content = &agentpbv2.DeviceDatagramFrame_IcmpRequest{IcmpRequest: &agentpbv2.DeviceIcmpEchoRequest{
			Identifier: request.GetIdentifier(), Sequence: request.GetSequence(),
			Payload: request.GetPayload(), OriginateUnixNs: request.GetOriginateUnixNs(),
		}}
	case msg.GetIcmpReply() != nil:
		reply := msg.GetIcmpReply()
		frame.Content = &agentpbv2.DeviceDatagramFrame_IcmpReply{IcmpReply: &agentpbv2.DeviceIcmpEchoReply{
			Identifier: reply.GetIdentifier(), Sequence: reply.GetSequence(),
			Payload: reply.GetPayload(), OriginateUnixNs: reply.GetOriginateUnixNs(),
			AgentUnixNs: reply.GetAgentUnixNs(),
		}}
	}
	return d.stream.Send(frame)
}

func (d *deviceFrameStream) Recv() (*cloudpbv2.TunnelData, error) {
	frame, err := d.stream.Recv()
	if err != nil {
		return nil, err
	}

	msg := &cloudpbv2.TunnelData{}
	switch content := frame.GetContent().(type) {
	case *agentpbv2.DeviceDatagramFrame_Datagram:
		msg.Datagram = &cloudpbv2.TunnelDatagram{
			FlowId:  content.Datagram.GetFlowId(),
			Port:    content.Datagram.GetPort(),
			Payload: content.Datagram.GetPayload(),
		}
	case *agentpbv2.DeviceDatagramFrame_IcmpRequest:
		msg.IcmpRequest = &cloudpbv2.IcmpEchoRequest{
			Identifier:      content.IcmpRequest.GetIdentifier(),
			Sequence:        content.IcmpRequest.GetSequence(),
			Payload:         content.IcmpRequest.GetPayload(),
			OriginateUnixNs: content.IcmpRequest.GetOriginateUnixNs(),
		}
	case *agentpbv2.DeviceDatagramFrame_IcmpReply:
		msg.IcmpReply = &cloudpbv2.IcmpEchoReply{
			Identifier:      content.IcmpReply.GetIdentifier(),
			Sequence:        content.IcmpReply.GetSequence(),
			Payload:         content.IcmpReply.GetPayload(),
			OriginateUnixNs: content.IcmpReply.GetOriginateUnixNs(),
			AgentUnixNs:     content.IcmpReply.GetAgentUnixNs(),
		}
	}
	return msg, nil
}
