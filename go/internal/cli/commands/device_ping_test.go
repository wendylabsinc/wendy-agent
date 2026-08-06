package commands

import (
	"bytes"
	"context"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// echoIcmpTunnelServer answers every icmp_request with a matching icmp_reply,
// standing in for TunnelService.DatagramTunnel's real echo behavior.
type echoIcmpTunnelServer struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
}

func (s *echoIcmpTunnelServer) DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		req := msg.GetIcmpRequest()
		if req == nil {
			continue
		}
		if err := stream.Send(&agentpbv2.DeviceDatagramFrame{
			Content: &agentpbv2.DeviceDatagramFrame_IcmpReply{IcmpReply: &agentpbv2.DeviceIcmpEchoReply{
				Identifier: req.GetIdentifier(), Sequence: req.GetSequence(),
				Payload: req.GetPayload(), OriginateUnixNs: req.GetOriginateUnixNs(),
				AgentUnixNs: uint64(time.Now().UnixNano()),
			}},
		}); err != nil {
			return err
		}
	}
}

func TestDevicePingRoundTrip(t *testing.T) {
	client := dialFakeAgent(t, &echoIcmpTunnelServer{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := openDeviceDatagramSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	var out bytes.Buffer
	stats := runPingLoop(ctx, session, "test-device", 2, 10*time.Millisecond, &out)
	if stats.Sent != 2 || stats.Received != 2 {
		t.Fatalf("stats = %+v, want 2 sent / 2 received", stats)
	}
}
