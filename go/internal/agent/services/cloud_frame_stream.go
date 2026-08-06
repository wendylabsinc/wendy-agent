package services

import (
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

// cloudFrameStream adapts the cloud broker's cloudpb.TunnelData stream
// (agentTunnelStream) to the protocol-agnostic tunnelframe.Stream the shared
// datagram-relay engine runs against.
type cloudFrameStream struct {
	stream agentTunnelStream
}

func (c cloudFrameStream) Send(f tunnelframe.Frame) error {
	msg := &cloudpb.TunnelData{}
	switch {
	case f.Datagram != nil:
		msg.Datagram = &cloudpb.TunnelDatagram{
			FlowId: f.Datagram.FlowID, Port: f.Datagram.Port, Payload: f.Datagram.Payload,
		}
	case f.IcmpRequest != nil:
		msg.IcmpRequest = &cloudpb.IcmpEchoRequest{
			Identifier: f.IcmpRequest.Identifier, Sequence: f.IcmpRequest.Sequence,
			Payload: f.IcmpRequest.Payload, OriginateUnixNs: f.IcmpRequest.OriginateUnixNs,
		}
	case f.IcmpReply != nil:
		msg.IcmpReply = &cloudpb.IcmpEchoReply{
			Identifier: f.IcmpReply.Identifier, Sequence: f.IcmpReply.Sequence,
			Payload: f.IcmpReply.Payload, OriginateUnixNs: f.IcmpReply.OriginateUnixNs,
			AgentUnixNs: f.IcmpReply.AgentUnixNs,
		}
	}
	return c.stream.Send(msg)
}

func (c cloudFrameStream) Recv() (tunnelframe.Frame, error) {
	msg, err := c.stream.Recv()
	if err != nil {
		return tunnelframe.Frame{}, err
	}
	var f tunnelframe.Frame
	switch {
	case msg.GetDatagram() != nil:
		d := msg.GetDatagram()
		f.Datagram = &tunnelframe.Datagram{FlowID: d.GetFlowId(), Port: d.GetPort(), Payload: d.GetPayload()}
	case msg.GetIcmpRequest() != nil:
		r := msg.GetIcmpRequest()
		f.IcmpRequest = &tunnelframe.IcmpEchoRequest{
			Identifier: r.GetIdentifier(), Sequence: r.GetSequence(),
			Payload: r.GetPayload(), OriginateUnixNs: r.GetOriginateUnixNs(),
		}
	case msg.GetIcmpReply() != nil:
		r := msg.GetIcmpReply()
		f.IcmpReply = &tunnelframe.IcmpEchoReply{
			Identifier: r.GetIdentifier(), Sequence: r.GetSequence(),
			Payload: r.GetPayload(), OriginateUnixNs: r.GetOriginateUnixNs(),
			AgentUnixNs: r.GetAgentUnixNs(),
		}
	}
	return f, nil
}
