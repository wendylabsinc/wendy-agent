package commands

import (
	"context"
	"fmt"
	"sync"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

// deviceDatagramSession is one DatagramTunnel stream to the selected LAN
// agent, implementing the same datagramSender/pingSession interfaces the
// cloud path's datagramSession does (cloud_datagram.go, cloud_ping.go).
type deviceDatagramSession struct {
	stream agentpbv2.WendyTunnelService_DatagramTunnelClient
	sendMu sync.Mutex
}

func openDeviceDatagramSession(ctx context.Context, client agentpbv2.WendyTunnelServiceClient) (*deviceDatagramSession, error) {
	stream, err := client.DatagramTunnel(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening device datagram tunnel: %w", err)
	}
	return &deviceDatagramSession{stream: stream}, nil
}

func (s *deviceDatagramSession) send(msg *agentpbv2.DeviceDatagramFrame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(msg)
}

func (s *deviceDatagramSession) sendDatagram(flowID, port uint32, payload []byte) error {
	return s.send(&agentpbv2.DeviceDatagramFrame{
		Content: &agentpbv2.DeviceDatagramFrame_Datagram{Datagram: &agentpbv2.DeviceDatagram{
			FlowId: flowID, Port: port, Payload: payload,
		}},
	})
}

func (s *deviceDatagramSession) sendEcho(req *tunnelframe.IcmpEchoRequest) error {
	return s.send(&agentpbv2.DeviceDatagramFrame{
		Content: &agentpbv2.DeviceDatagramFrame_IcmpRequest{IcmpRequest: &agentpbv2.DeviceIcmpEchoRequest{
			Identifier: req.Identifier, Sequence: req.Sequence,
			Payload: req.Payload, OriginateUnixNs: req.OriginateUnixNs,
		}},
	})
}

func (s *deviceDatagramSession) recv() (tunnelframe.Frame, error) {
	msg, err := s.stream.Recv()
	if err != nil {
		return tunnelframe.Frame{}, err
	}
	var f tunnelframe.Frame
	switch c := msg.GetContent().(type) {
	case *agentpbv2.DeviceDatagramFrame_Datagram:
		f.Datagram = &tunnelframe.Datagram{
			FlowID: c.Datagram.GetFlowId(), Port: c.Datagram.GetPort(), Payload: c.Datagram.GetPayload(),
		}
	case *agentpbv2.DeviceDatagramFrame_IcmpReply:
		f.IcmpReply = &tunnelframe.IcmpEchoReply{
			Identifier: c.IcmpReply.GetIdentifier(), Sequence: c.IcmpReply.GetSequence(),
			Payload: c.IcmpReply.GetPayload(), OriginateUnixNs: c.IcmpReply.GetOriginateUnixNs(),
			AgentUnixNs: c.IcmpReply.GetAgentUnixNs(),
		}
	}
	return f, nil
}

func (s *deviceDatagramSession) close() { _ = s.stream.CloseSend() }
