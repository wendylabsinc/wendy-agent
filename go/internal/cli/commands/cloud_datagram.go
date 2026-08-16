package commands

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// udpFlowIdleTimeout is the client-edge flow lifetime: a local UDP peer that
// stays silent this long is forgotten (the agent side keeps a 2min safety net).
const udpFlowIdleTimeout = 60 * time.Second

// datagramSession is one multiplexed DATAGRAM tunnel to a device, carrying
// all UDP flows and ICMP echoes for that (client, device) pair.
type datagramSession struct {
	stream cloudpb.TunnelBrokerService_ClientTunnelClient
	sendMu sync.Mutex
}

// datagramSender is the subset used by serveUDPForward, split out for tests.
type datagramSender interface {
	sendDatagram(flowID, port uint32, payload []byte) error
	recv() (tunnelframe.Frame, error)
}

func openDatagramSession(ctx context.Context, brokerConn *grpc.ClientConn, auth *config.AuthConfig, assetID int32) (*datagramSession, error) {
	client := cloudpb.NewTunnelBrokerServiceClient(brokerConn)
	stream, err := client.ClientTunnel(cloudContext(ctx, auth))
	if err != nil {
		return nil, fmt.Errorf("opening datagram session: %w", err)
	}
	if err := stream.Send(&cloudpb.ClientTunnelMessage{
		Content: &cloudpb.ClientTunnelMessage_Open{
			Open: &cloudpb.ClientTunnelOpen{
				AssetId:  assetID,
				Host:     "localhost",
				Protocol: cloudpb.TunnelProtocol_TUNNEL_PROTOCOL_DATAGRAM,
			},
		},
	}); err != nil {
		return nil, fmt.Errorf("sending datagram open: %w", err)
	}
	return &datagramSession{stream: stream}, nil
}

func (s *datagramSession) send(msg *cloudpb.TunnelData) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(&cloudpb.ClientTunnelMessage{
		Content: &cloudpb.ClientTunnelMessage_Data{Data: msg},
	})
}

func (s *datagramSession) sendDatagram(flowID, port uint32, payload []byte) error {
	return s.send(&cloudpb.TunnelData{Datagram: &cloudpb.TunnelDatagram{
		FlowId: flowID, Port: port, Payload: payload,
	}})
}

func (s *datagramSession) sendEcho(req *tunnelframe.IcmpEchoRequest) error {
	return s.send(&cloudpb.TunnelData{IcmpRequest: &cloudpb.IcmpEchoRequest{
		Identifier: req.Identifier, Sequence: req.Sequence,
		Payload: req.Payload, OriginateUnixNs: req.OriginateUnixNs,
	}})
}

func (s *datagramSession) recv() (tunnelframe.Frame, error) {
	msg, err := s.stream.Recv()
	if err != nil {
		return tunnelframe.Frame{}, err
	}
	var f tunnelframe.Frame
	if d := msg.GetDatagram(); d != nil {
		f.Datagram = &tunnelframe.Datagram{FlowID: d.GetFlowId(), Port: d.GetPort(), Payload: d.GetPayload()}
	}
	if r := msg.GetIcmpReply(); r != nil {
		f.IcmpReply = &tunnelframe.IcmpEchoReply{
			Identifier: r.GetIdentifier(), Sequence: r.GetSequence(),
			Payload: r.GetPayload(), OriginateUnixNs: r.GetOriginateUnixNs(), AgentUnixNs: r.GetAgentUnixNs(),
		}
	}
	return f, nil
}

func (s *datagramSession) close() { _ = s.stream.CloseSend() }

// datagramOpenError maps the classic old-agent failure (the agent ignores the
// protocol field, dials TCP, and never claims the session) to a helpful hint.
func datagramOpenError(err error, device string) error {
	if s, ok := status.FromError(err); ok &&
		(s.Code() == codes.DeadlineExceeded || s.Code() == codes.Unavailable || s.Code() == codes.Unimplemented) {
		return fmt.Errorf("%s did not answer the datagram session (%v); the device may be offline or need a WendyOS update for UDP/ping tunnel support", device, err)
	}
	return err
}

// serveUDPForward pumps datagrams between a local UDP listener and a datagram
// session. Flows are keyed by local source address; the first packet from a
// new source allocates a flow_id, replies route back by source, and entries
// expire after `idle` without traffic.
//
// It returns nil when `ctx` is what ended the loop (normal shutdown, e.g.
// Ctrl+C), or the error that killed the session/listener otherwise — callers
// use this to tell "user stopped it" from "the tunnel died underneath us"
// (see cloudTunnelCommand, which folds a non-nil result through
// datagramOpenError for the WendyOS-update hint).
func serveUDPForward(ctx context.Context, pc *net.UDPConn, session datagramSender, remotePort uint32, idle time.Duration) error {
	type flowEntry struct {
		addr       *net.UDPAddr
		lastActive time.Time
	}
	var (
		mu      sync.Mutex
		nextID  uint32 = 1
		byAddr         = map[string]uint32{}
		byID           = map[uint32]*flowEntry{}
		lifeErr error  // first error that ended the session, guarded by mu
	)
	setLifeErr := func(err error) {
		mu.Lock()
		if lifeErr == nil {
			lifeErr = err
		}
		mu.Unlock()
	}

	// done ties the idle-sweep goroutine's lifetime to this call, not just to
	// ctx: without it, a session death (not a ctx cancellation) would leave
	// the sweep goroutine parked on ctx.Done() until process exit.
	done := make(chan struct{})
	defer close(done)

	// Session → local peers.
	go func() {
		for {
			f, err := session.recv()
			if err != nil {
				setLifeErr(err)
				pc.Close() // unblocks the read loop below
				return
			}
			d := f.Datagram
			if d == nil {
				continue
			}
			mu.Lock()
			entry := byID[d.FlowID]
			if entry != nil {
				entry.lastActive = time.Now()
			}
			mu.Unlock()
			if entry != nil {
				_, _ = pc.WriteToUDP(d.Payload, entry.addr)
			}
		}
	}()

	// Idle sweep.
	go func() {
		t := time.NewTicker(idle / 4)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mu.Lock()
				for id, e := range byID {
					if time.Since(e.lastActive) > idle {
						delete(byAddr, e.addr.String())
						delete(byID, id)
					}
				}
				mu.Unlock()
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}
	}()

	// Local peers → session.
	buf := make([]byte, 65535)
	for {
		n, addr, err := pc.ReadFromUDP(buf)
		if err != nil {
			break // listener closed (ctx cancel or session death)
		}
		key := addr.String()
		mu.Lock()
		id, ok := byAddr[key]
		if !ok {
			id = nextID
			nextID++
			byAddr[key] = id
			byID[id] = &flowEntry{addr: addr, lastActive: time.Now()}
		} else {
			byID[id].lastActive = time.Now()
		}
		mu.Unlock()
		payload := make([]byte, n)
		copy(payload, buf[:n])
		if err := session.sendDatagram(id, remotePort, payload); err != nil {
			setLifeErr(err)
			break
		}
	}

	if ctx.Err() != nil {
		return nil
	}
	mu.Lock()
	err := lifeErr
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("datagram session closed unexpectedly: %w", err)
	}
	return nil
}
