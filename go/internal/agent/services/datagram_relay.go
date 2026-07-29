package services

import (
	"context"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

const (
	// datagramFlowIdleTimeout is the agent-side safety expiry for UDP flow
	// sockets; the client edge owns the primary (60s) flow lifetime.
	datagramFlowIdleTimeout = 2 * time.Minute
	maxUDPPayload           = 65507
)

// datagramRelay serves one DATAGRAM tunnel session: a flow table of connected
// loopback UDP sockets keyed by client-assigned flow_id, plus inline ICMP echo
// replies (the agent IS the pinged host; no ICMP socket is involved).
type datagramRelay struct {
	logger      *zap.Logger
	stream      agentTunnelStream
	idleTimeout time.Duration

	sendMu sync.Mutex // gRPC streams do not allow concurrent Send

	mu    sync.Mutex
	flows map[uint32]*datagramFlow

	lastOversizeLog time.Time
}

type datagramFlow struct {
	conn       *net.UDPConn
	port       uint32
	lastActive time.Time // guarded by datagramRelay.mu
}

func newDatagramRelay(logger *zap.Logger, stream agentTunnelStream, idleTimeout time.Duration) *datagramRelay {
	return &datagramRelay{
		logger:      logger,
		stream:      stream,
		idleTimeout: idleTimeout,
		flows:       make(map[uint32]*datagramFlow),
	}
}

func (r *datagramRelay) activeFlows() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.flows)
}

func (r *datagramRelay) send(msg *cloudpb.TunnelData) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	return r.stream.Send(msg)
}

// run serves the session until the stream ends or ctx is cancelled.
func (r *datagramRelay) run(ctx context.Context) {
	sweep := time.NewTicker(r.idleTimeout / 4)
	defer sweep.Stop()
	defer r.closeAll()

	frames := make(chan *cloudpb.TunnelData)
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := r.stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case frames <- msg:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case msg := <-frames:
			switch {
			case msg.GetDatagram() != nil:
				r.handleDatagram(ctx, msg.GetDatagram())
			case msg.GetIcmpRequest() != nil:
				r.handleEcho(msg.GetIcmpRequest())
			}
		case <-sweep.C:
			r.expireIdle()
		case <-recvErr:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (r *datagramRelay) handleEcho(req *cloudpb.IcmpEchoRequest) {
	err := r.send(&cloudpb.TunnelData{IcmpReply: &cloudpb.IcmpEchoReply{
		Identifier:      req.GetIdentifier(),
		Sequence:        req.GetSequence(),
		Payload:         req.GetPayload(),
		OriginateUnixNs: req.GetOriginateUnixNs(),
		AgentUnixNs:     uint64(time.Now().UnixNano()),
	}})
	if err != nil {
		r.logger.Warn("failed to send icmp echo reply", zap.Error(err))
	}
}

func (r *datagramRelay) handleDatagram(ctx context.Context, d *cloudpb.TunnelDatagram) {
	if len(d.GetPayload()) > maxUDPPayload {
		r.mu.Lock()
		if time.Since(r.lastOversizeLog) > 10*time.Second {
			r.lastOversizeLog = time.Now()
			r.logger.Warn("dropping oversized tunnel datagram",
				zap.Uint32("flow_id", d.GetFlowId()), zap.Int("size", len(d.GetPayload())))
		}
		r.mu.Unlock()
		return
	}

	flow, err := r.flow(ctx, d.GetFlowId(), d.GetPort())
	if err != nil {
		r.logger.Warn("failed to open UDP flow",
			zap.Uint32("flow_id", d.GetFlowId()), zap.Uint32("port", d.GetPort()), zap.Error(err))
		return
	}
	if _, err := flow.conn.Write(d.GetPayload()); err != nil {
		r.closeFlow(d.GetFlowId())
	}
}

func (r *datagramRelay) flow(ctx context.Context, flowID, port uint32) (*datagramFlow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.flows[flowID]; ok {
		f.lastActive = time.Now()
		return f, nil
	}
	conn, err := net.DialUDP("udp",
		nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		return nil, err
	}
	f := &datagramFlow{conn: conn, port: port, lastActive: time.Now()}
	r.flows[flowID] = f
	go r.readFlow(ctx, flowID, f)
	return f, nil
}

// readFlow pumps device→client datagrams for one flow until its socket closes.
func (r *datagramRelay) readFlow(ctx context.Context, flowID uint32, f *datagramFlow) {
	buf := make([]byte, maxUDPPayload)
	for {
		n, err := f.conn.Read(buf)
		if err != nil {
			r.closeFlow(flowID)
			return
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		r.mu.Lock()
		f.lastActive = time.Now()
		r.mu.Unlock()
		if err := r.send(&cloudpb.TunnelData{Datagram: &cloudpb.TunnelDatagram{
			FlowId: flowID, Port: f.port, Payload: payload,
		}}); err != nil {
			r.closeFlow(flowID)
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (r *datagramRelay) closeFlow(flowID uint32) {
	r.mu.Lock()
	f, ok := r.flows[flowID]
	delete(r.flows, flowID)
	r.mu.Unlock()
	if ok {
		_ = f.conn.Close()
	}
}

func (r *datagramRelay) expireIdle() {
	r.mu.Lock()
	var expired []uint32
	for id, f := range r.flows {
		if time.Since(f.lastActive) > r.idleTimeout {
			expired = append(expired, id)
		}
	}
	r.mu.Unlock()
	for _, id := range expired {
		r.closeFlow(id)
	}
}

func (r *datagramRelay) closeAll() {
	r.mu.Lock()
	flows := r.flows
	r.flows = make(map[uint32]*datagramFlow)
	r.mu.Unlock()
	for _, f := range flows {
		_ = f.conn.Close()
	}
}
