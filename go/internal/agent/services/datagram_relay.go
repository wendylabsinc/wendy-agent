package services

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

const (
	// datagramFlowIdleTimeout is the agent-side safety expiry for UDP flow
	// sockets; the client edge owns the primary (60s) flow lifetime.
	datagramFlowIdleTimeout = 2 * time.Minute
	maxUDPPayload           = 65507

	// maxFlowsPerSession bounds how many concurrent UDP sockets (each backed
	// by its own goroutine) one datagram session will open. flow_id is
	// entirely client-assigned and unauthenticated beyond session membership,
	// so without a cap a buggy or hostile same-org client could walk flow_id
	// and exhaust the device's file descriptors / goroutines. 256 comfortably
	// covers the multi-flow use cases this design targets (a handful of UDP
	// streams plus ICMP per session) while bounding worst-case resource use
	// per session to a few hundred sockets.
	maxFlowsPerSession = 256

	// rateLimitLogInterval bounds how often a single client can force a log
	// line for the same recurring condition (oversize datagram, flow-table
	// cap, dial failure) — mirrors the pre-existing lastOversizeLog pattern.
	rateLimitLogInterval = 10 * time.Second
)

// errFlowCapReached is returned by flow() when a brand-new flow_id would push
// the session over maxFlowsPerSession. It never applies to an already-open
// flow_id (that path is just a write to an existing socket, not a new fd).
var errFlowCapReached = errors.New("datagram flow-table cap reached")

// datagramRelay serves one DATAGRAM tunnel session: a flow table of connected
// loopback UDP sockets keyed by client-assigned flow_id, plus inline ICMP echo
// replies (the agent IS the pinged host; no ICMP socket is involved). It runs
// against tunnelframe.Stream so it is shared between the cloud broker path
// and the LAN device path.
type datagramRelay struct {
	logger      *zap.Logger
	stream      tunnelframe.Stream
	idleTimeout time.Duration

	sendMu sync.Mutex // gRPC streams do not allow concurrent Send

	mu    sync.Mutex
	flows map[uint32]*datagramFlow

	lastOversizeLog    time.Time
	lastFlowCapLog     time.Time
	lastDialFailLog    time.Time
	lastInvalidPortLog time.Time
}

type datagramFlow struct {
	conn       *net.UDPConn
	port       uint32
	lastActive time.Time // guarded by datagramRelay.mu
}

func newDatagramRelay(logger *zap.Logger, stream tunnelframe.Stream, idleTimeout time.Duration) *datagramRelay {
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

func (r *datagramRelay) send(f tunnelframe.Frame) error {
	r.sendMu.Lock()
	defer r.sendMu.Unlock()
	return r.stream.Send(f)
}

// run serves the session until the stream ends or ctx is cancelled.
func (r *datagramRelay) run(ctx context.Context) {
	sweep := time.NewTicker(r.idleTimeout / 4)
	defer sweep.Stop()
	defer r.closeAll()

	frames := make(chan tunnelframe.Frame)
	recvErr := make(chan error, 1)
	go func() {
		for {
			f, err := r.stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case f := <-frames:
			switch {
			case f.Datagram != nil:
				r.handleDatagram(ctx, f.Datagram)
			case f.IcmpRequest != nil:
				r.handleEcho(f.IcmpRequest)
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

func (r *datagramRelay) handleEcho(req *tunnelframe.IcmpEchoRequest) {
	err := r.send(tunnelframe.Frame{IcmpReply: &tunnelframe.IcmpEchoReply{
		Identifier:      req.Identifier,
		Sequence:        req.Sequence,
		Payload:         req.Payload,
		OriginateUnixNs: req.OriginateUnixNs,
		AgentUnixNs:     uint64(time.Now().UnixNano()),
	}})
	if err != nil {
		r.logger.Warn("failed to send icmp echo reply", zap.Error(err))
	}
}

func (r *datagramRelay) handleDatagram(ctx context.Context, d *tunnelframe.Datagram) {
	if len(d.Payload) > maxUDPPayload {
		r.mu.Lock()
		if time.Since(r.lastOversizeLog) > rateLimitLogInterval {
			r.lastOversizeLog = time.Now()
			r.logger.Warn("dropping oversized tunnel datagram",
				zap.Uint32("flow_id", d.FlowID), zap.Int("size", len(d.Payload)))
		}
		r.mu.Unlock()
		return
	}

	if d.Port == 0 || d.Port > 65535 {
		r.mu.Lock()
		if time.Since(r.lastInvalidPortLog) > rateLimitLogInterval {
			r.lastInvalidPortLog = time.Now()
			r.logger.Warn("dropping datagram: invalid port",
				zap.Uint32("flow_id", d.FlowID), zap.Uint32("port", d.Port))
		}
		r.mu.Unlock()
		return
	}

	flow, err := r.flow(ctx, d.FlowID, d.Port)
	if err != nil {
		if errors.Is(err, errFlowCapReached) {
			r.mu.Lock()
			if time.Since(r.lastFlowCapLog) > rateLimitLogInterval {
				r.lastFlowCapLog = time.Now()
				r.logger.Warn("dropping datagram: session flow-table cap reached",
					zap.Uint32("flow_id", d.FlowID), zap.Int("max_flows", maxFlowsPerSession))
			}
			r.mu.Unlock()
			return
		}
		r.mu.Lock()
		if time.Since(r.lastDialFailLog) > rateLimitLogInterval {
			r.lastDialFailLog = time.Now()
			r.logger.Warn("failed to open UDP flow",
				zap.Uint32("flow_id", d.FlowID), zap.Uint32("port", d.Port), zap.Error(err))
		}
		r.mu.Unlock()
		return
	}
	if _, err := flow.conn.Write(d.Payload); err != nil {
		r.closeFlow(d.FlowID)
	}
}

func (r *datagramRelay) flow(ctx context.Context, flowID, port uint32) (*datagramFlow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.flows[flowID]; ok {
		f.lastActive = time.Now()
		return f, nil
	}
	if len(r.flows) >= maxFlowsPerSession {
		return nil, errFlowCapReached
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
		if err := r.send(tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
			FlowID: flowID, Port: f.port, Payload: payload,
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
