# LAN Tunnel UDP + ICMP Ping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add UDP port forwarding (`wendy device tunnel <port>/udp`) and ICMP-style ping (`wendy device ping`) to the LAN device tunnel, matching the capability #1520 added to the cloud tunnel — via a new dedicated `DatagramTunnel` RPC (not by overloading the existing TCP `Tunnel` RPC), with the flow-table/ICMP-echo engine shared between the cloud and LAN paths instead of duplicated.

**Architecture:** A new protocol-agnostic `tunnelframe` package (plain Go structs, no proto dependency) becomes the common seam. The existing cloud datagram-relay engine (`go/internal/agent/services/datagram_relay.go`, from #1520) is refactored to run against `tunnelframe.Frame`/`tunnelframe.Stream` instead of `cloudpb.TunnelData` directly, via a thin `cloudFrameStream` adapter. A new `DatagramTunnel` RPC on `WendyTunnelService` (agent-side, LAN-only, mTLS-registered like the existing `Tunnel` RPC) reuses that same engine via a `deviceFrameStream` adapter. CLI-side, `serveUDPForward`/`runPingLoop` (from #1520) are broadened the same way, and new `device_datagram.go`/`device_ping.go` files add the LAN-side commands.

**Tech Stack:** Go, gRPC bidi streaming, protobuf (protoc + protoc-gen-go/protoc-gen-go-grpc), Cobra CLI, `go test` (table-driven + bufconn for in-process gRPC tests).

## Global Constraints

- The `Tunnel` RPC and `DeviceTunnelRequest`/`DeviceTunnelData` messages (#1533's existing TCP LAN tunnel) must not change at all.
- `DatagramTunnel` must be registered exactly where `Tunnel` is registered today: the mTLS server only (`go/cmd/wendy-agent/main.go`), never the unauthenticated pre-provisioning server or the local admin socket.
- ICMP ping is agent-self-echo only — the agent answers `icmp_request` with `icmp_reply` immediately; it never opens a raw socket or pings any other host. This matches #1520's cloud-path precedent exactly.
- Reuse the existing tunable constants from #1520 unchanged: `maxUDPPayload = 65507`, `maxFlowsPerSession = 256`, `datagramFlowIdleTimeout = 2 * time.Minute` (agent-side), `udpFlowIdleTimeout = 60 * time.Second` (CLI-side), `rateLimitLogInterval = 10 * time.Second`.
- No behavior change to the existing cloud tunnel (`wendy cloud tunnel`, `wendy cloud ping`) is permitted — the refactor tasks (3, 4, 6) must leave cloud-path tests green with no functional differences, only a different internal representation.
- This branch (`jo/lan-tunnel-udp-icmp`) is stacked on Oliver's `codex/foxglove-lan-cloud-integration` (#1533) and pulls in Joannis's own `feat/tunnel-udp-icmp` (#1520) via Task 1's merge — do not push to either of those branches directly.

---

### Task 1: Merge #1520's cloud UDP/ICMP branch into this one

#1533 (this branch's base) doesn't have #1520's cloud datagram-relay code yet — it's needed as the baseline the later refactor tasks operate on. Both branches share a common ancestor and merge cleanly (verified during planning).

**Files:** none (git operation only)

- [ ] **Step 1: Merge `feat/tunnel-udp-icmp` into the current branch**

```bash
git merge feat/tunnel-udp-icmp -m "Merge feat/tunnel-udp-icmp (#1520 cloud UDP/ICMP) into LAN tunnel work"
```

- [ ] **Step 2: Verify the merge brought in the expected files with no conflicts**

Run: `git show --stat HEAD | tail -20`
Expected: no `CONFLICT` output from the merge itself, and the diffstat includes `go/internal/agent/services/datagram_relay.go`, `go/internal/cli/commands/cloud_datagram.go`, `go/internal/cli/commands/cloud_ping.go`, `Proto/cloud/tunnel.proto`, `go/proto/gen/cloudpb/tunnel.pb.go`.

- [ ] **Step 3: Build and test to confirm the merged tree is healthy**

Run: `cd go && go build ./... && go test ./internal/agent/services/... ./internal/cli/commands/... -count=1`
Expected: PASS (this is #1520's own already-passing test suite, just now living on this branch).

---

### Task 2: Add the `DatagramTunnel` RPC and its messages to the LAN tunnel proto

**Files:**
- Modify: `Proto/wendy/agent/services/v2/tunnel_service.proto`
- Modify: `go/proto/gen/agentpb/v2/tunnel_service.pb.go` (generated)
- Modify: `go/proto/gen/agentpb/v2/tunnel_service_grpc.pb.go` (generated)

**Interfaces:**
- Produces: `agentpbv2.WendyTunnelService_DatagramTunnelServer` (agent-side stream, `Send(*agentpbv2.DeviceDatagramFrame) error` / `Recv() (*agentpbv2.DeviceDatagramFrame, error)` / `Context() context.Context`), `agentpbv2.WendyTunnelServiceClient.DatagramTunnel(ctx) (agentpbv2.WendyTunnelService_DatagramTunnelClient, error)`, `agentpbv2.DeviceDatagramFrame` with oneof wrapper types `DeviceDatagramFrame_Datagram`, `DeviceDatagramFrame_IcmpRequest`, `DeviceDatagramFrame_IcmpReply`.

- [ ] **Step 1: Add the new RPC and messages to the proto file**

Modify `Proto/wendy/agent/services/v2/tunnel_service.proto` — replace its full contents with:

```proto
syntax = "proto3";

package wendy.agent.services.v2;

option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";

// WendyTunnelService forwards authenticated client streams to the agent's
// loopback interface. It is the LAN counterpart to the Cloud tunnel broker
// and never accepts an arbitrary target host.
service WendyTunnelService {
  rpc Tunnel(stream DeviceTunnelRequest) returns (stream DeviceTunnelData);

  // DatagramTunnel multiplexes many UDP flows and ICMP-echo pings over one
  // stream, keyed by client-assigned flow_id. Unlike Tunnel, there is no
  // open/session handshake: this is already a direct stream to one
  // already-selected, already-authenticated agent (no broker rendezvous),
  // so the first frame is just a normal datagram or icmp_request frame.
  rpc DatagramTunnel(stream DeviceDatagramFrame) returns (stream DeviceDatagramFrame);
}

// First client message on a Tunnel stream: which agent-local port to connect.
message DeviceTunnelOpen {
  uint32 port = 1;
}

message DeviceTunnelData {
  bytes payload = 1;
  bool half_close = 2;
}

// Client → server framing: first message must be open; subsequent messages are
// data frames.
message DeviceTunnelRequest {
  oneof content {
    DeviceTunnelOpen open = 1;
    DeviceTunnelData data = 2;
  }
}

// Exactly one field set per frame, in either direction.
message DeviceDatagramFrame {
  oneof content {
    DeviceDatagram        datagram     = 1;
    DeviceIcmpEchoRequest icmp_request = 2;
    DeviceIcmpEchoReply   icmp_reply   = 3;
  }
}

message DeviceDatagram {
  uint32 flow_id = 1;
  uint32 port    = 2;  // destination port on the device's loopback interface
  bytes  payload = 3;  // one UDP datagram, max 65507 bytes
}

message DeviceIcmpEchoRequest {
  uint32 identifier = 1;
  uint32 sequence   = 2;
  bytes  payload    = 3;
  uint64 originate_unix_ns = 4;
}

message DeviceIcmpEchoReply {
  uint32 identifier = 1;         // copied from request
  uint32 sequence   = 2;         // copied from request
  bytes  payload    = 3;         // echoed verbatim
  uint64 originate_unix_ns = 4;  // copied from request
  uint64 agent_unix_ns     = 5;  // agent receive time
}
```

- [ ] **Step 2: Install the proto codegen plugins pinned to this repo's versions**

Run:
```bash
cd go && go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12-0.20260120151049-f2248ac996af
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```
Expected: both binaries land in `$(go env GOPATH)/bin` (already on `PATH` per `generate-proto.sh`'s own `export PATH`).

- [ ] **Step 3: Regenerate protos**

Run: `cd go && bash scripts/generate-proto.sh`
Expected: exits 0; `git status` shows changes under `go/proto/gen/agentpb/v2/tunnel_service.pb.go` and `tunnel_service_grpc.pb.go` (new message types and the new RPC method), with no other `agentpb`/`cloudpb` files touched.

- [ ] **Step 4: Build to confirm the generated code compiles and nothing else broke**

Run: `cd go && go build ./...`
Expected: PASS. (`TunnelService` doesn't implement `DatagramTunnel` yet — that's fine, `agentpbv2.UnimplementedWendyTunnelServiceServer` embedded in `TunnelService` satisfies the interface with a default `Unimplemented` stub until Task 5.)

- [ ] **Step 5: Commit**

```bash
git add Proto/wendy/agent/services/v2/tunnel_service.proto go/proto/gen/agentpb/v2/tunnel_service.pb.go go/proto/gen/agentpb/v2/tunnel_service_grpc.pb.go
git commit -m "proto: add DatagramTunnel RPC for LAN UDP/ICMP tunnel"
```

---

### Task 3: Add the shared `tunnelframe` package

Plain data types and a stream interface with zero proto dependency — the seam both the cloud and LAN datagram-relay code will run against. There's no behavior to unit-test here (no logic, just struct/interface declarations); a build check is the right verification for this task.

**Files:**
- Create: `go/internal/shared/tunnelframe/frame.go`

**Interfaces:**
- Produces: `tunnelframe.Frame{Datagram *Datagram, IcmpRequest *IcmpEchoRequest, IcmpReply *IcmpEchoReply}`, `tunnelframe.Datagram{FlowID, Port uint32, Payload []byte}`, `tunnelframe.IcmpEchoRequest{Identifier, Sequence uint32, Payload []byte, OriginateUnixNs uint64}`, `tunnelframe.IcmpEchoReply{Identifier, Sequence uint32, Payload []byte, OriginateUnixNs, AgentUnixNs uint64}`, `tunnelframe.Stream{Send(Frame) error; Recv() (Frame, error)}`.

- [ ] **Step 1: Create the package**

```go
// Package tunnelframe defines a protocol-agnostic datagram-tunnel frame,
// decoupled from any generated proto type, so the flow-table/ICMP-echo
// relay engine and the CLI's UDP-forward/ping loops can be shared between
// the cloud tunnel broker path and the LAN device tunnel path. Each side
// provides a thin adapter converting its own generated proto stream type
// to/from Frame.
package tunnelframe

// Frame is one message in either direction of a datagram-tunnel session.
// Exactly one field is set.
type Frame struct {
	Datagram    *Datagram
	IcmpRequest *IcmpEchoRequest
	IcmpReply   *IcmpEchoReply
}

// Datagram is one UDP datagram, multiplexed by client-assigned FlowID.
type Datagram struct {
	FlowID, Port uint32
	Payload      []byte
}

// IcmpEchoRequest asks the agent to answer as itself: a reply proves the
// agent is alive with a true end-to-end RTT. No ICMP socket is involved.
type IcmpEchoRequest struct {
	Identifier, Sequence uint32
	Payload              []byte
	OriginateUnixNs      uint64
}

// IcmpEchoReply echoes an IcmpEchoRequest's Identifier/Sequence/Payload/
// OriginateUnixNs verbatim and stamps AgentUnixNs at reply time.
type IcmpEchoReply struct {
	Identifier, Sequence         uint32
	Payload                      []byte
	OriginateUnixNs, AgentUnixNs uint64
}

// Stream is the transport-agnostic seam the shared relay engine and CLI
// loops run against.
type Stream interface {
	Send(Frame) error
	Recv() (Frame, error)
}
```

- [ ] **Step 2: Build to confirm it compiles**

Run: `cd go && go build ./internal/shared/tunnelframe/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add go/internal/shared/tunnelframe/frame.go
git commit -m "feat: add shared tunnelframe package for datagram-relay engine"
```

---

### Task 4: Refactor the cloud datagram relay onto `tunnelframe`

Rewrites `datagramRelay` (agent-side, cloud path) to run against `tunnelframe.Frame`/`tunnelframe.Stream` instead of `*cloudpb.TunnelData`/`agentTunnelStream` directly, with a `cloudFrameStream` adapter bridging the two at the one call site that constructs it. This is a pure refactor — no behavior change — verified by the existing (retargeted) test suite passing.

**Files:**
- Modify: `go/internal/agent/services/datagram_relay.go`
- Modify: `go/internal/agent/services/datagram_relay_test.go`
- Create: `go/internal/agent/services/cloud_frame_stream.go`
- Modify: `go/internal/agent/services/tunnel_broker_client.go:314-329` (`handleDatagramDial`)

**Interfaces:**
- Consumes: `tunnelframe.Frame`, `tunnelframe.Stream`, `tunnelframe.Datagram`, `tunnelframe.IcmpEchoRequest`, `tunnelframe.IcmpEchoReply` (Task 3).
- Produces: `newDatagramRelay(logger *zap.Logger, stream tunnelframe.Stream, idleTimeout time.Duration) *datagramRelay` (signature changed: `stream` is now `tunnelframe.Stream`, not `agentTunnelStream`), `cloudFrameStream{stream agentTunnelStream}` implementing `tunnelframe.Stream`.

- [ ] **Step 1: Rewrite `datagram_relay.go` against `tunnelframe`**

Replace the full contents of `go/internal/agent/services/datagram_relay.go`:

```go
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

	lastOversizeLog time.Time
	lastFlowCapLog  time.Time
	lastDialFailLog time.Time
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
```

- [ ] **Step 2: Add the `cloudFrameStream` adapter**

Create `go/internal/agent/services/cloud_frame_stream.go`:

```go
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
```

- [ ] **Step 3: Update the one call site that constructs a `datagramRelay` for the cloud path**

Modify `go/internal/agent/services/tunnel_broker_client.go`, in `handleDatagramDial` — change:

```go
	c.logger.Info("serving datagram session", zap.String("session_id", req.SessionId))
	newDatagramRelay(c.logger, agentStream, datagramFlowIdleTimeout).run(callCtx)
```

to:

```go
	c.logger.Info("serving datagram session", zap.String("session_id", req.SessionId))
	newDatagramRelay(c.logger, cloudFrameStream{stream: agentStream}, datagramFlowIdleTimeout).run(callCtx)
```

- [ ] **Step 4: Retarget `datagram_relay_test.go` onto `tunnelframe`**

Replace the full contents of `go/internal/agent/services/datagram_relay_test.go`:

```go
package services

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

type fakeFrameStream struct {
	ctx context.Context
	in  chan tunnelframe.Frame
	out chan tunnelframe.Frame
}

func newFakeFrameStream(ctx context.Context) *fakeFrameStream {
	return &fakeFrameStream{
		ctx: ctx,
		in:  make(chan tunnelframe.Frame, 16),
		out: make(chan tunnelframe.Frame, 16),
	}
}
func (f *fakeFrameStream) Send(fr tunnelframe.Frame) error {
	select {
	case f.out <- fr:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeFrameStream) Recv() (tunnelframe.Frame, error) {
	select {
	case fr, ok := <-f.in:
		if !ok {
			return tunnelframe.Frame{}, io.EOF
		}
		return fr, nil
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}

// startUDPEcho starts a loopback UDP echo server and returns its port.
func startUDPEcho(t *testing.T) uint32 {
	t.Helper()
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
	return uint32(pc.LocalAddr().(*net.UDPAddr).Port)
}

func awaitFrame(t *testing.T, f *fakeFrameStream) tunnelframe.Frame {
	t.Helper()
	select {
	case fr := <-f.out:
		return fr
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for relay output frame")
		return tunnelframe.Frame{}
	}
}

func TestDatagramRelayUDPRoundTrip(t *testing.T) {
	port := startUDPEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 7, Port: port, Payload: []byte("hello"),
	}}

	reply := awaitFrame(t, stream)
	if reply.Datagram == nil {
		t.Fatalf("expected datagram frame, got %+v", reply)
	}
	if got := reply.Datagram.FlowID; got != 7 {
		t.Fatalf("flow_id = %d, want 7", got)
	}
	if got := string(reply.Datagram.Payload); got != "hello" {
		t.Fatalf("payload = %q, want hello", got)
	}
}

func TestDatagramRelayEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{IcmpRequest: &tunnelframe.IcmpEchoRequest{
		Identifier: 0x1234, Sequence: 3, Payload: []byte("ping"), OriginateUnixNs: 42,
	}}

	reply := awaitFrame(t, stream)
	r := reply.IcmpReply
	if r == nil {
		t.Fatalf("expected icmp_reply frame, got %+v", reply)
	}
	if r.Identifier != 0x1234 || r.Sequence != 3 ||
		string(r.Payload) != "ping" || r.OriginateUnixNs != 42 {
		t.Fatalf("echo fields not copied: %+v", r)
	}
	if r.AgentUnixNs == 0 {
		t.Fatal("agent_unix_ns not stamped")
	}
}

func TestDatagramRelayIdleExpiry(t *testing.T) {
	port := startUDPEcho(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, 50*time.Millisecond)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 1, Port: port, Payload: []byte("x"),
	}}
	awaitFrame(t, stream) // echo comes back → flow exists

	deadline := time.Now().Add(3 * time.Second)
	for relay.activeFlows() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("flow not expired; activeFlows=%d", relay.activeFlows())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestDatagramRelayFlowCap verifies flow_id — entirely client-controlled —
// cannot open unbounded sockets/goroutines on the agent: the
// maxFlowsPerSession+1th distinct flow_id is rejected with
// errFlowCapReached (no socket dialed), while re-using an already-open
// flow_id past the cap still succeeds (it's a write, not a new fd).
func TestDatagramRelayFlowCap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)

	// UDP connect() doesn't require a listener on the far end, so distinct
	// loopback ports below are fine to dial without an echo server.
	for i := uint32(0); i < maxFlowsPerSession; i++ {
		if _, err := relay.flow(ctx, i+1, 9); err != nil {
			t.Fatalf("flow %d: unexpected error opening flow within cap: %v", i+1, err)
		}
	}
	if got := relay.activeFlows(); got != maxFlowsPerSession {
		t.Fatalf("activeFlows = %d, want %d", got, maxFlowsPerSession)
	}

	if _, err := relay.flow(ctx, maxFlowsPerSession+100, 9); !errors.Is(err, errFlowCapReached) {
		t.Fatalf("flow beyond cap: err = %v, want errFlowCapReached", err)
	}
	if got := relay.activeFlows(); got != maxFlowsPerSession {
		t.Fatalf("activeFlows after rejected flow = %d, want unchanged %d", got, maxFlowsPerSession)
	}

	// An already-open flow_id is a write to an existing socket, not a new
	// fd, so it must succeed even though the session is at the cap.
	if _, err := relay.flow(ctx, 1, 9); err != nil {
		t.Fatalf("re-using an already-open flow_id at cap: unexpected error: %v", err)
	}
}

func TestDatagramRelayDropsOversized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeFrameStream(ctx)
	relay := newDatagramRelay(zap.NewNop(), stream, time.Minute)
	go relay.run(ctx)

	stream.in <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: 1, Port: 9, Payload: make([]byte, maxUDPPayload+1),
	}}
	// Follow with a valid echo; if the oversized frame had opened a flow or
	// crashed the loop, this would not come back.
	stream.in <- tunnelframe.Frame{IcmpRequest: &tunnelframe.IcmpEchoRequest{Identifier: 1, Sequence: 1}}
	reply := awaitFrame(t, stream)
	if reply.IcmpReply == nil {
		t.Fatalf("relay loop broken after oversized datagram: %+v", reply)
	}
	if relay.activeFlows() != 0 {
		t.Fatalf("oversized datagram opened a flow")
	}
}
```

- [ ] **Step 5: Run the retargeted tests**

Run: `cd go && go test ./internal/agent/services/... -run TestDatagramRelay -v -count=1`
Expected: PASS (all 5 tests) — same behavior as before the refactor, now exercised through `tunnelframe.Frame` directly instead of `cloudpb.TunnelData`.

- [ ] **Step 6: Run the full agent/services package tests to confirm the cloud path still works end-to-end**

Run: `cd go && go test ./internal/agent/services/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/datagram_relay.go go/internal/agent/services/datagram_relay_test.go go/internal/agent/services/cloud_frame_stream.go go/internal/agent/services/tunnel_broker_client.go
git commit -m "refactor(agent): run datagram relay against shared tunnelframe.Stream"
```

---

### Task 5: Implement `TunnelService.DatagramTunnel`

Wires the LAN tunnel's new RPC to the same shared relay engine via a `deviceFrameStream` adapter.

**Files:**
- Modify: `go/internal/agent/services/tunnel_service.go`
- Modify: `go/internal/agent/services/tunnel_service_test.go`

**Interfaces:**
- Consumes: `newDatagramRelay(logger, stream tunnelframe.Stream, idleTimeout) *datagramRelay` (Task 4), `agentpbv2.WendyTunnelService_DatagramTunnelServer` (Task 2).
- Produces: `TunnelService.DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error`.

- [ ] **Step 1: Write the failing test**

Add to `go/internal/agent/services/tunnel_service_test.go` (append; keep the existing imports and tests):

```go
type fakeDeviceDatagramStream struct {
	agentpbv2.WendyTunnelService_DatagramTunnelServer
	ctx    context.Context
	in     chan *agentpbv2.DeviceDatagramFrame
	out    chan *agentpbv2.DeviceDatagramFrame
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
```

Add `"io"` and `"time"` to the test file's import block if not already present (both are already imported per the existing file).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd go && go test ./internal/agent/services/... -run TestTunnelServiceDatagramTunnel -v -count=1`
Expected: FAIL with `svc.DatagramTunnel undefined (type *TunnelService has no field or method DatagramTunnel)`.

- [ ] **Step 3: Implement `DatagramTunnel`**

Add to `go/internal/agent/services/tunnel_service.go` (after the existing `Tunnel` method, before `relay`):

```go
// DatagramTunnel serves one multiplexed UDP + ICMP-echo session for the LAN
// tunnel, sharing the same flow-table engine as the cloud broker's datagram
// path (see datagram_relay.go). There is no open/session handshake — this is
// already a direct stream to one already-authenticated agent.
func (s *TunnelService) DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error {
	newDatagramRelay(s.logger, deviceFrameStream{stream: stream}, datagramFlowIdleTimeout).run(stream.Context())
	return nil
}

// deviceFrameStream adapts the LAN tunnel's agentpbv2.DeviceDatagramFrame
// stream to the protocol-agnostic tunnelframe.Stream the shared
// datagram-relay engine runs against.
type deviceFrameStream struct {
	stream agentpbv2.WendyTunnelService_DatagramTunnelServer
}

func (d deviceFrameStream) Send(f tunnelframe.Frame) error {
	msg := &agentpbv2.DeviceDatagramFrame{}
	switch {
	case f.Datagram != nil:
		msg.Content = &agentpbv2.DeviceDatagramFrame_Datagram{Datagram: &agentpbv2.DeviceDatagram{
			FlowId: f.Datagram.FlowID, Port: f.Datagram.Port, Payload: f.Datagram.Payload,
		}}
	case f.IcmpRequest != nil:
		msg.Content = &agentpbv2.DeviceDatagramFrame_IcmpRequest{IcmpRequest: &agentpbv2.DeviceIcmpEchoRequest{
			Identifier: f.IcmpRequest.Identifier, Sequence: f.IcmpRequest.Sequence,
			Payload: f.IcmpRequest.Payload, OriginateUnixNs: f.IcmpRequest.OriginateUnixNs,
		}}
	case f.IcmpReply != nil:
		msg.Content = &agentpbv2.DeviceDatagramFrame_IcmpReply{IcmpReply: &agentpbv2.DeviceIcmpEchoReply{
			Identifier: f.IcmpReply.Identifier, Sequence: f.IcmpReply.Sequence,
			Payload: f.IcmpReply.Payload, OriginateUnixNs: f.IcmpReply.OriginateUnixNs,
			AgentUnixNs: f.IcmpReply.AgentUnixNs,
		}}
	}
	return d.stream.Send(msg)
}

func (d deviceFrameStream) Recv() (tunnelframe.Frame, error) {
	msg, err := d.stream.Recv()
	if err != nil {
		return tunnelframe.Frame{}, err
	}
	var f tunnelframe.Frame
	switch c := msg.GetContent().(type) {
	case *agentpbv2.DeviceDatagramFrame_Datagram:
		f.Datagram = &tunnelframe.Datagram{
			FlowID: c.Datagram.GetFlowId(), Port: c.Datagram.GetPort(), Payload: c.Datagram.GetPayload(),
		}
	case *agentpbv2.DeviceDatagramFrame_IcmpRequest:
		f.IcmpRequest = &tunnelframe.IcmpEchoRequest{
			Identifier: c.IcmpRequest.GetIdentifier(), Sequence: c.IcmpRequest.GetSequence(),
			Payload: c.IcmpRequest.GetPayload(), OriginateUnixNs: c.IcmpRequest.GetOriginateUnixNs(),
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
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"` to `tunnel_service.go`'s import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/... -run TestTunnelServiceDatagramTunnel -v -count=1`
Expected: PASS (both tests).

- [ ] **Step 5: Run the full package test suite**

Run: `cd go && go test ./internal/agent/services/... -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/tunnel_service.go go/internal/agent/services/tunnel_service_test.go
git commit -m "feat(agent): serve UDP + ICMP-echo over DatagramTunnel"
```

---

### Task 6: Register `DatagramTunnel` — confirm it rides along with `Tunnel`'s existing registration

`WendyTunnelService` is registered once (`agentpb.RegisterWendyTunnelServiceServer`, wait — `agentpbv2.RegisterWendyTunnelServiceServer(srv, tunnelSvc)` in `go/cmd/wendy-agent/main.go`) on the mTLS server only; since `DatagramTunnel` is a second method on the same service, no new registration call is needed. This task only verifies that.

**Files:** none modified (verification only)

- [ ] **Step 1: Confirm there is exactly one registration call and it's on the mTLS server**

Run: `cd go && grep -n "RegisterWendyTunnelServiceServer" cmd/wendy-agent/main.go`
Expected: exactly one match, inside the mTLS-server block (the same block that also registers `WendyShellService`), per the comment there about not exposing it on the pre-provisioning server or admin socket.

- [ ] **Step 2: Build the agent binary to confirm it links**

Run: `cd go && go build ./cmd/wendy-agent/...`
Expected: PASS.

(No commit — this task makes no file changes.)

---

### Task 7: Broaden the CLI's cloud datagram/ping sessions onto `tunnelframe`

Same refactor as Task 4, on the CLI side: `datagramSender` and `pingSession` (and the concrete `datagramSession` that implements both) move from `*cloudpb.TunnelData` to `tunnelframe.Frame`. Pure refactor, no behavior change.

**Files:**
- Modify: `go/internal/cli/commands/cloud_datagram.go`
- Modify: `go/internal/cli/commands/cloud_datagram_test.go`
- Modify: `go/internal/cli/commands/cloud_ping.go`
- Modify: `go/internal/cli/commands/cloud_ping_test.go`

**Interfaces:**
- Consumes: `tunnelframe.Frame`, `tunnelframe.Datagram`, `tunnelframe.IcmpEchoRequest`, `tunnelframe.IcmpEchoReply` (Task 3).
- Produces: `datagramSender{sendDatagram(flowID, port uint32, payload []byte) error; recv() (tunnelframe.Frame, error)}`, `pingSession{sendEcho(req *tunnelframe.IcmpEchoRequest) error; recv() (tunnelframe.Frame, error)}`, `datagramSession` implementing both.

- [ ] **Step 1: Rewrite `cloud_datagram.go`'s session type and `serveUDPForward` against `tunnelframe`**

Replace the full contents of `go/internal/cli/commands/cloud_datagram.go`:

```go
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
		(s.Code() == codes.DeadlineExceeded || s.Code() == codes.Unavailable) {
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
```

- [ ] **Step 2: Retarget `cloud_datagram_test.go`'s fake onto `tunnelframe`**

In `go/internal/cli/commands/cloud_datagram_test.go`, replace the `fakeDatagramSession` type and its methods (the `TestParseTunnelArgUDPSuffix` test and `TestServeUDPForwardRoundTrip` stay as-is):

```go
type fakeDatagramSession struct {
	ctx    context.Context
	frames chan tunnelframe.Frame
}

func newFakeDatagramSession(ctx context.Context) *fakeDatagramSession {
	return &fakeDatagramSession{ctx: ctx, frames: make(chan tunnelframe.Frame, 64)}
}
func (f *fakeDatagramSession) sendDatagram(flowID, port uint32, payload []byte) error {
	select {
	case f.frames <- tunnelframe.Frame{Datagram: &tunnelframe.Datagram{
		FlowID: flowID, Port: port, Payload: payload,
	}}:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeDatagramSession) recv() (tunnelframe.Frame, error) {
	select {
	case fr, ok := <-f.frames:
		if !ok {
			return tunnelframe.Frame{}, io.EOF
		}
		return fr, nil
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}
```

Add `"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"` to the test file's import block, and remove the now-unused `cloudpb` import if `TestParseTunnelArgUDPSuffix` doesn't reference it (check: it doesn't — that test only touches `parseTunnelArg`'s primitive return values).

- [ ] **Step 3: Rewrite `cloud_ping.go`'s `pingSession` interface and `runPingLoop` against `tunnelframe`**

Replace the full contents of `go/internal/cli/commands/cloud_ping.go`:

```go
package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

// pingSession is the slice of datagramSession that runPingLoop needs.
type pingSession interface {
	sendEcho(req *tunnelframe.IcmpEchoRequest) error
	recv() (tunnelframe.Frame, error)
}

type pingStats struct {
	Sent, Received int
	Min, Avg, Max  time.Duration
	// Err is the transport error (if any) that ended the recv loop — e.g.
	// PermissionDenied, Unauthenticated, or a mesh-disabled rejection. Nil
	// means the recv loop never errored (a silent device, or the ping ended
	// normally). Callers should only fall back to the generic "may need a
	// WendyOS update" hint when this is nil; otherwise surface it (through
	// datagramOpenError, which still special-cases DeadlineExceeded/
	// Unavailable into the same hint).
	Err error
}

func newCloudPingCmd() *cobra.Command {
	var cloudGRPC, deviceName, brokerURL string
	var count int
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "ping [--device <name>]",
		Short: "Ping a cloud-enrolled device through the tunnel broker",
		Long:  "Sends echo requests over a Wendy Cloud datagram tunnel session. A reply proves the device's agent is up and measures true end-to-end round-trip time. No ICMP sockets or privileges are involved.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cloudPingCommand(cmd.Context(), cloudGRPC, deviceName, brokerURL, count, interval)
		},
	}
	cmd.Flags().StringVar(&cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is set via 'wendy auth use')")
	cmd.Flags().StringVar(&deviceName, "device", "", "Device name (skips interactive picker)")
	cmd.Flags().StringVar(&brokerURL, "broker-url", os.Getenv("WENDY_BROKER_URL"), "Tunnel broker host:port")
	cmd.Flags().IntVarP(&count, "count", "c", 0, "Stop after this many echoes (0 = until Ctrl+C)")
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "Time between echoes")
	return cmd
}

func cloudPingCommand(ctx context.Context, cloudGRPC, deviceName, brokerURL string, count int, interval time.Duration) error {
	auth, err := pickAuthEntry(cloudGRPC)
	if err != nil {
		return err
	}
	asset, err := pickCloudDevice(ctx, auth, deviceName, brokerURL)
	if err != nil {
		return err
	}
	brokerConn, err := dialCloudBroker(auth, brokerURL)
	if err != nil {
		return err
	}
	defer brokerConn.Close()

	session, err := openDatagramSession(ctx, brokerConn, auth, asset.GetId())
	if err != nil {
		return datagramOpenError(err, asset.GetName())
	}
	defer session.close()

	cliLogln("PING %s (asset %d) via Wendy Cloud", asset.GetName(), asset.GetId())
	stats := runPingLoop(ctx, session, asset.GetName(), count, interval, os.Stdout)

	loss := 0.0
	if stats.Sent > 0 {
		loss = 100 * float64(stats.Sent-stats.Received) / float64(stats.Sent)
	}
	cliLogln("--- %s ping statistics ---", asset.GetName())
	cliLogln("%d sent, %d received, %.0f%% loss", stats.Sent, stats.Received, loss)
	if stats.Received > 0 {
		cliLogln("rtt min/avg/max = %s/%s/%s", stats.Min.Round(time.Microsecond),
			stats.Avg.Round(time.Microsecond), stats.Max.Round(time.Microsecond))
	}
	if stats.Received == 0 && stats.Sent > 0 {
		if stats.Err != nil {
			return datagramOpenError(stats.Err, asset.GetName())
		}
		return fmt.Errorf("no replies from %s: the device may be offline or need a WendyOS update for ping support", asset.GetName())
	}
	return nil
}

// runPingLoop sends one echo per interval (count of 0 = unbounded), prints
// each matched reply to out, and returns aggregate stats once every echo has
// either been answered or the ctx ends. Replies are matched to a pending
// request by sequence number; only replies carrying this process's identifier
// are considered (so two concurrent ping runs against the same device don't
// cross-count each other's replies).
func runPingLoop(ctx context.Context, session pingSession, target string, count int, interval time.Duration, out io.Writer) pingStats {
	identifier := uint32(os.Getpid() & 0xFFFF)
	type sentEcho struct{ originate time.Time }
	var (
		stats     pingStats
		pending   = map[uint32]sentEcho{} // keyed by sequence
		total     time.Duration
		done      = make(chan struct{})
		replies   = make(chan *tunnelframe.IcmpEchoReply, 16)
		lifeErrMu sync.Mutex
		lifeErr   error // first transport error out of the recv loop, guarded by lifeErrMu
	)
	setLifeErr := func(err error) {
		lifeErrMu.Lock()
		if lifeErr == nil {
			lifeErr = err
		}
		lifeErrMu.Unlock()
	}

	go func() {
		defer close(done)
		for {
			f, err := session.recv()
			if err != nil {
				setLifeErr(err)
				return
			}
			if r := f.IcmpReply; r != nil && r.Identifier == identifier {
				select {
				case replies <- r:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	seq := uint32(0)
	// sendOne returns false once the requested count has been dispatched (or
	// on a transport error), which the caller uses to stop the send phase.
	// stats.Sent and pending are only updated after sendEcho actually
	// succeeds, so a transport failure doesn't get counted as a lost reply.
	sendOne := func() bool {
		if count > 0 && int(seq) >= count {
			return false
		}
		seq++
		now := time.Now()
		req := &tunnelframe.IcmpEchoRequest{
			Identifier:      identifier,
			Sequence:        seq,
			Payload:         []byte("wendy-ping"),
			OriginateUnixNs: uint64(now.UnixNano()),
		}
		if err := session.sendEcho(req); err != nil {
			return false
		}
		pending[seq] = sentEcho{originate: now}
		stats.Sent++
		return true
	}
	if !sendOne() {
		return stats
	}

	// Grace window after the last send so the final reply(ies) can still
	// arrive; armed exactly once, when the send phase ends (all echoes
	// dispatched, or a transport error stopped it early).
	graceTimer := time.NewTimer(24 * time.Hour)
	defer graceTimer.Stop()
	sendingDone := false
	armGrace := func() {
		if sendingDone {
			return // already armed — a re-fired ticker.C must not push the deadline out again
		}
		sendingDone = true
		ticker.Stop()
		grace := interval
		if grace < 500*time.Millisecond {
			grace = 500 * time.Millisecond
		}
		graceTimer.Reset(grace)
	}

	finish := func() pingStats {
		stats.Avg = pingAvg(total, stats.Received)
		lifeErrMu.Lock()
		stats.Err = lifeErr
		lifeErrMu.Unlock()
		return stats
	}

	for {
		select {
		case r := <-replies:
			if s, ok := pending[r.Sequence]; ok {
				delete(pending, r.Sequence)
				rtt := time.Since(s.originate)
				stats.Received++
				total += rtt
				if stats.Min == 0 || rtt < stats.Min {
					stats.Min = rtt
				}
				if rtt > stats.Max {
					stats.Max = rtt
				}
				fmt.Fprintf(out, "reply from %s: seq=%d time=%s\n", target, r.Sequence, rtt.Round(time.Microsecond))
			}
			if count > 0 && len(pending) == 0 && stats.Sent >= count {
				return finish()
			}
		case <-ticker.C:
			if !sendOne() {
				armGrace()
			}
		case <-graceTimer.C:
			return finish()
		case <-ctx.Done():
			return finish()
		case <-done:
			return finish()
		}
	}
}

func pingAvg(total time.Duration, n int) time.Duration {
	if n == 0 {
		return 0
	}
	return total / time.Duration(n)
}
```

- [ ] **Step 4: Retarget `cloud_ping_test.go`'s fakes onto `tunnelframe`**

Replace `fakePingSession` and `failingPingSession` in `go/internal/cli/commands/cloud_ping_test.go` (the three `TestRunPingLoop*` tests stay as-is, calling the same `runPingLoop` signature):

```go
package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/tunnelframe"
)

// fakePingSession answers every echo request immediately. It selects on ctx
// everywhere it can block so a cancelled test context always unblocks both
// sendEcho and recv, instead of leaking a goroutine parked on an idle channel
// past test end (see fakeDatagramSession in cloud_datagram_test.go and
// fakeFrameStream in go/internal/agent/services/datagram_relay_test.go for
// the same pattern).
type fakePingSession struct {
	ctx     context.Context
	mu      sync.Mutex
	replies chan tunnelframe.Frame
	drop    bool // when true, swallow requests (packet loss)
}

func newFakePingSession(ctx context.Context) *fakePingSession {
	return &fakePingSession{ctx: ctx, replies: make(chan tunnelframe.Frame, 16)}
}
func (f *fakePingSession) sendEcho(req *tunnelframe.IcmpEchoRequest) error {
	f.mu.Lock()
	drop := f.drop
	f.mu.Unlock()
	if drop {
		return nil
	}
	reply := tunnelframe.Frame{IcmpReply: &tunnelframe.IcmpEchoReply{
		Identifier:      req.Identifier,
		Sequence:        req.Sequence,
		Payload:         req.Payload,
		OriginateUnixNs: req.OriginateUnixNs,
		AgentUnixNs:     uint64(time.Now().UnixNano()),
	}}
	select {
	case f.replies <- reply:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakePingSession) recv() (tunnelframe.Frame, error) {
	select {
	case fr, ok := <-f.replies:
		if !ok {
			return tunnelframe.Frame{}, io.EOF
		}
		return fr, nil
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}

// failingPingSession never delivers a reply; its recv() returns recvErr once
// a request has been sent, simulating a genuine transport failure
// (PermissionDenied, Unauthenticated, mesh-disabled, ...) rather than silence.
type failingPingSession struct {
	ctx     context.Context
	recvErr error
	sent    chan struct{}
	once    sync.Once
}

func newFailingPingSession(ctx context.Context, recvErr error) *failingPingSession {
	return &failingPingSession{ctx: ctx, recvErr: recvErr, sent: make(chan struct{})}
}
func (f *failingPingSession) sendEcho(*tunnelframe.IcmpEchoRequest) error {
	f.once.Do(func() { close(f.sent) })
	return nil
}
func (f *failingPingSession) recv() (tunnelframe.Frame, error) {
	select {
	case <-f.sent:
		return tunnelframe.Frame{}, f.recvErr
	case <-f.ctx.Done():
		return tunnelframe.Frame{}, f.ctx.Err()
	}
}
```

(keep the file's existing `TestRunPingLoopCountsReplies`, `TestRunPingLoopCountsLoss`, and `TestRunPingLoopSurfacesTransportError` bodies unchanged below this — they call `runPingLoop` and the fakes' constructors, not their internals, so they don't need edits.)

- [ ] **Step 5: Run the CLI package tests**

Run: `cd go && go test ./internal/cli/commands/... -run 'TestParseTunnelArg|TestServeUDPForward|TestRunPingLoop' -v -count=1`
Expected: PASS (all of them — same behavior, now via `tunnelframe.Frame`).

- [ ] **Step 6: Run the full CLI commands package test suite**

Run: `cd go && go test ./internal/cli/commands/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/cloud_datagram.go go/internal/cli/commands/cloud_datagram_test.go go/internal/cli/commands/cloud_ping.go go/internal/cli/commands/cloud_ping_test.go
git commit -m "refactor(cli): run cloud UDP forward and ping against shared tunnelframe"
```

---

### Task 8: Add `wendy device tunnel <port>/udp` (LAN UDP forwarding)

`device_tunnel.go` already parses the `/udp` suffix via `parseTunnelArg` (shared with `wendy cloud tunnel`) but discards the `udp` return value. This task wires it up and adds the LAN-side `deviceDatagramSession` adapter.

**Files:**
- Create: `go/internal/cli/commands/device_datagram.go`
- Create: `go/internal/cli/commands/device_datagram_test.go`
- Modify: `go/internal/cli/commands/device_tunnel.go`

**Interfaces:**
- Consumes: `serveUDPForward(ctx, pc *net.UDPConn, session datagramSender, remotePort uint32, idle time.Duration) error` (Task 7), `agentpbv2.WendyTunnelServiceClient.DatagramTunnel(ctx) (agentpbv2.WendyTunnelService_DatagramTunnelClient, error)` (Task 2), `target.Agent.TunnelService agentpbv2.WendyTunnelServiceClient` (existing, `go/internal/cli/grpcclient/client.go:70`).
- Produces: `deviceDatagramSession` implementing `datagramSender` and `pingSession`; `openDeviceDatagramSession(ctx, client agentpbv2.WendyTunnelServiceClient) (*deviceDatagramSession, error)`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/device_datagram_test.go`:

```go
package commands

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// echoDatagramTunnelServer echoes every datagram frame straight back,
// standing in for a real agent's DatagramTunnel handler.
type echoDatagramTunnelServer struct {
	agentpbv2.UnimplementedWendyTunnelServiceServer
}

func (s *echoDatagramTunnelServer) DatagramTunnel(stream agentpbv2.WendyTunnelService_DatagramTunnelServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
}

func dialFakeAgent(t *testing.T, server agentpbv2.WendyTunnelServiceServer) agentpbv2.WendyTunnelServiceClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	agentpbv2.RegisterWendyTunnelServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return agentpbv2.NewWendyTunnelServiceClient(conn)
}

func TestDeviceDatagramSessionUDPRoundTrip(t *testing.T) {
	client := dialFakeAgent(t, &echoDatagramTunnelServer{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	session, err := openDeviceDatagramSession(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()

	if err := session.sendDatagram(4, 9000, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	f, err := session.recv()
	if err != nil {
		t.Fatal(err)
	}
	if f.Datagram == nil || f.Datagram.FlowID != 4 || string(f.Datagram.Payload) != "hello" {
		t.Fatalf("unexpected frame: %+v", f)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/... -run TestDeviceDatagramSessionUDPRoundTrip -v -count=1`
Expected: FAIL with `undefined: openDeviceDatagramSession`.

- [ ] **Step 3: Implement `device_datagram.go`**

Create `go/internal/cli/commands/device_datagram.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/... -run TestDeviceDatagramSessionUDPRoundTrip -v -count=1`
Expected: PASS.

- [ ] **Step 5: Wire `wendy device tunnel <port>/udp` into `device_tunnel.go`**

Modify `go/internal/cli/commands/device_tunnel.go`:

Change the `RunE` closure to capture `udp`:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, cloud := cloudDeviceConfigFromContext(cmd.Context()); cloud {
				return fmt.Errorf("use 'wendy cloud tunnel' for cloud-connected devices")
			}
			localPort, remotePort, udp, err := parseTunnelArg(args[0])
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return deviceTunnelCommand(ctx, localPort, remotePort, udp)
		},
```

Update the `Use`/`Short`/`Long` strings to match the cloud command's UDP-aware wording:

```go
		Use:   "tunnel <local-port>:<remote-port>[/udp]",
		Short: "Forward a local TCP or UDP port through the selected LAN agent",
		Long:  "Listens on local loopback and forwards each connection or datagram through the selected LAN agent to a port on the device's loopback interface.",
```

Change `deviceTunnelCommand` to branch on `udp`:

```go
func deviceTunnelCommand(ctx context.Context, localPort, remotePort uint32, udp bool) error {
	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return err
	}
	defer target.Close()
	if target.Agent == nil || target.Agent.TunnelService == nil {
		return fmt.Errorf("device tunnel requires a WendyOS LAN agent")
	}

	if udp {
		pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(localPort)})
		if err != nil {
			return fmt.Errorf("listening on udp 127.0.0.1:%d: %w", localPort, err)
		}
		defer pc.Close()
		session, err := openDeviceDatagramSession(ctx, target.Agent.TunnelService)
		if err != nil {
			return fmt.Errorf("opening device datagram tunnel: %w", err)
		}
		defer session.close()
		cliSuccess("Forwarding udp 127.0.0.1:%d → %s:%d (via LAN agent)", localPort, target.Agent.Host, remotePort)
		cliLogln("Press Ctrl+C to stop.")
		go func() { <-ctx.Done(); pc.Close() }()
		return serveUDPForward(ctx, pc, session, remotePort, udpFlowIdleTimeout)
	}

	listener, err := listenDeviceTunnel(localPort)
	if err != nil {
		return err
	}
	defer listener.Close()
	return serveDeviceTunnel(ctx, target.Agent, listener, remotePort)
}
```

Add `"net"` and `"time"` are already imported by this file per its existing content; no import changes needed beyond what's already there (`net` is already imported for `listenDeviceTunnel`).

- [ ] **Step 6: Update `device_tunnel_test.go`'s call site for the new signature**

`device_tunnel_test.go` calls `openDeviceTunnel` and `serveDeviceTunnel`, not `deviceTunnelCommand` directly, so it needs no changes — confirm by running it (next step).

- [ ] **Step 7: Run the full CLI commands package tests**

Run: `cd go && go test ./internal/cli/commands/... -count=1`
Expected: PASS.

- [ ] **Step 8: Manual validation against a real LAN agent**

Run (with a WendyOS device on the LAN, some UDP service listening on the device, e.g. an echo service on port 9000):
```bash
wendy device tunnel 9000/udp --device <hostname>
# in another terminal:
echo -n "hello" | nc -u -w1 127.0.0.1 9000
```
Expected: the forwarded UDP datagram round-trips.

- [ ] **Step 9: Commit**

```bash
git add go/internal/cli/commands/device_datagram.go go/internal/cli/commands/device_datagram_test.go go/internal/cli/commands/device_tunnel.go
git commit -m "feat(cli): wendy device tunnel <port>/udp over DatagramTunnel"
```

---

### Task 9: Add `wendy device ping`

Mirrors `wendy cloud ping`, reusing the shared `runPingLoop` (Task 7) against a `deviceDatagramSession` (Task 8).

**Files:**
- Create: `go/internal/cli/commands/device_ping.go`
- Create: `go/internal/cli/commands/device_ping_test.go`
- Modify: `go/internal/cli/commands/device.go`

**Interfaces:**
- Consumes: `runPingLoop(ctx, session pingSession, target string, count int, interval time.Duration, out io.Writer) pingStats` (Task 7), `openDeviceDatagramSession(ctx, client agentpbv2.WendyTunnelServiceClient) (*deviceDatagramSession, error)` (Task 8), `resolveTarget(ctx, opts ...) (*Target, error)` (existing, used by `device_tunnel.go`).
- Produces: `newDevicePingCmd() *cobra.Command`, `devicePingCommand(ctx context.Context, count int, interval time.Duration) error`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/device_ping_test.go`:

```go
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
```

- [ ] **Step 2: Run the test**

Run: `cd go && go test ./internal/cli/commands/... -run TestDevicePingRoundTrip -v -count=1`
Expected: PASS. This test only exercises plumbing Tasks 7–8 already built (`deviceDatagramSession` + shared `runPingLoop`), so it passes immediately — it's here to lock that plumbing in before wiring the actual `wendy device ping` command around it, which is the deliverable the next steps add.

- [ ] **Step 3: Implement `device_ping.go`**

Create `go/internal/cli/commands/device_ping.go`:

```go
package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newDevicePingCmd() *cobra.Command {
	var count int
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "ping",
		Short: "Ping the selected LAN device through its agent connection",
		Long:  "Sends echo requests over the device's DatagramTunnel session. A reply proves the device's agent is up and measures true end-to-end round-trip time. No ICMP sockets or privileges are involved.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return devicePingCommand(cmd.Context(), count, interval)
		},
	}
	cmd.Flags().IntVarP(&count, "count", "c", 0, "Stop after this many echoes (0 = until Ctrl+C)")
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "Time between echoes")
	return cmd
}

func devicePingCommand(ctx context.Context, count int, interval time.Duration) error {
	target, err := resolveTarget(ctx, ExcludeBluetooth())
	if err != nil {
		return err
	}
	defer target.Close()
	if target.Agent == nil || target.Agent.TunnelService == nil {
		return fmt.Errorf("device ping requires a WendyOS LAN agent")
	}

	session, err := openDeviceDatagramSession(ctx, target.Agent.TunnelService)
	if err != nil {
		return fmt.Errorf("opening device datagram tunnel: %w", err)
	}
	defer session.close()

	cliLogln("PING %s (via LAN agent)", target.Agent.Host)
	stats := runPingLoop(ctx, session, target.Agent.Host, count, interval, os.Stdout)

	loss := 0.0
	if stats.Sent > 0 {
		loss = 100 * float64(stats.Sent-stats.Received) / float64(stats.Sent)
	}
	cliLogln("--- %s ping statistics ---", target.Agent.Host)
	cliLogln("%d sent, %d received, %.0f%% loss", stats.Sent, stats.Received, loss)
	if stats.Received > 0 {
		cliLogln("rtt min/avg/max = %s/%s/%s", stats.Min.Round(time.Microsecond),
			stats.Avg.Round(time.Microsecond), stats.Max.Round(time.Microsecond))
	}
	if stats.Received == 0 && stats.Sent > 0 {
		if stats.Err != nil {
			return stats.Err
		}
		return fmt.Errorf("no replies from %s: the device may be offline or need a WendyOS update for ping support", target.Agent.Host)
	}
	return nil
}
```

- [ ] **Step 4: Register the command**

Modify `go/internal/cli/commands/device.go` — add `newDevicePingCmd()` to the `"common"` group, right after `newDeviceTunnelCmd()`:

```go
	addToGroup("common",
		newAppsCmd(),
		newDeviceLogsCmd(),
		newDeviceOSLogsCmd(),
		newROS2Cmd(),
		newFoxgloveCmd(),
		newDeviceTunnelCmd(),
		newDevicePingCmd(),
		newDeviceDashboardCmd(),
		newTopCmd(),
	)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/... -run TestDevicePing -v -count=1`
Expected: PASS.

- [ ] **Step 6: Run the full CLI commands package tests**

Run: `cd go && go test ./internal/cli/commands/... -count=1`
Expected: PASS.

- [ ] **Step 7: Manual validation against a real LAN agent**

Run: `wendy device ping --device <hostname>`
Expected: prints `reply from <host>: seq=N time=...` lines and final loss/RTT stats; Ctrl+C stops it cleanly.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/device_ping.go go/internal/cli/commands/device_ping_test.go go/internal/cli/commands/device.go
git commit -m "feat(cli): add wendy device ping over DatagramTunnel"
```

---

### Task 10: Documentation

**Files:**
- Create: `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/ping.md`
- Modify: `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/tunnel.md`
- Modify: `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/meta.json`

- [ ] **Step 1: Update `tunnel.md` for UDP support**

Replace the full contents of `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/tunnel.md`:

```markdown
# `wendy device tunnel`

Forward a TCP or UDP port from developer-machine loopback to a port on the
selected LAN device's loopback interface.

```bash
wendy device tunnel <local-port>:<remote-port>[/udp] [--device <hostname>]
```

When both ports are the same, a single value is sufficient:

```bash
wendy device tunnel 8765 --device woof.local
```

Add `/udp` for UDP forwarding (docker-style suffix, matching `wendy cloud tunnel`):

```bash
wendy device tunnel 9000/udp --device woof.local
```

The CLI listens only on `127.0.0.1`. TCP connections are relayed byte-for-byte
over the authenticated Wendy agent connection; UDP datagrams are multiplexed
over the same connection, keyed by source address, with idle flows expiring
after 60 seconds of silence. The agent dials only `127.0.0.1:<remote-port>`
(TCP) or a loopback UDP socket on that port — the remote service does not
need to bind to the device's LAN interfaces.

For cloud-enrolled devices that are not being reached directly on the LAN, use
[`wendy cloud tunnel`](../cloud/tunnel.md) instead.

## See also

- [`wendy device ping`](./ping.md) — check LAN-agent liveness and round-trip time over the same connection.
```

- [ ] **Step 2: Add `ping.md`**

Create `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/ping.md`:

```markdown
# `wendy device ping`

Ping the selected LAN device through its agent connection.

```bash
wendy device ping [--device <hostname>] [--count N] [--interval DURATION]
```

Each echo is answered by the device's own agent — there is no ICMP socket or
raw-socket privilege involved, and the device never pings anything else. A
reply proves the agent process is alive and measures a true end-to-end
round-trip time over the authenticated Wendy connection, not just network
reachability.

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--count`, `-c` | `0` | Stop after this many echoes. `0` means run until Ctrl+C. |
| `--interval`, `-i` | `1s` | Time between echoes. |

## Examples

Ping until interrupted:

```bash
wendy device ping --device woof.local
```

Send exactly 5 echoes:

```bash
wendy device ping --device woof.local --count 5
```

## See also

- [`wendy device tunnel`](./tunnel.md) — forward TCP or UDP ports over the same connection.
- [`wendy cloud ping`](../cloud/ping.md) — the cloud-tunnel equivalent for devices not reachable on the LAN.
```

- [ ] **Step 3: Add `ping` to `meta.json`**

Modify `go/internal/cli/assets/docs/clients/wendy-cli/commands/device/meta.json` — insert `"ping"` right after `"tunnel"`:

```json
{
  "pages": [
    "apps",
    "logs",
    "os-logs",
    "dashboard",
    "top",
    "info",
    "tunnel",
    "ping",
    "set-default",
    "unset-default",
    "update",
    "volumes",
    "wifi",
    "bluetooth",
    "---Reference---",
    "enroll",
    "rename",
    "setup",
    "get-default",
    "provision",
    "telemetry-stream",
    "version"
  ]
}
```

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/assets/docs/clients/wendy-cli/commands/device/ping.md go/internal/cli/assets/docs/clients/wendy-cli/commands/device/tunnel.md go/internal/cli/assets/docs/clients/wendy-cli/commands/device/meta.json
git commit -m "docs: document wendy device ping and UDP tunnel forwarding"
```

---

### Task 11: Full-repo verification

**Files:** none (verification only)

- [ ] **Step 1: Full build**

Run: `cd go && go build ./...`
Expected: PASS.

- [ ] **Step 2: Full vet**

Run: `cd go && go vet ./...`
Expected: PASS (no new warnings).

- [ ] **Step 3: Full test suite**

Run: `cd go && go test ./... -count=1 -timeout 120s`
Expected: PASS.

- [ ] **Step 4: Race-detector run on the two packages this work touches most**

Run: `cd go && go test ./internal/agent/services/... ./internal/cli/commands/... -race -count=1 -timeout 120s`
Expected: PASS (the flow table and idle-sweep goroutines are exactly the kind of code a race detector should check).

- [ ] **Step 5: Manual end-to-end validation against a real LAN agent**

With a WendyOS device on the LAN:
```bash
wendy device ping --device <hostname>
wendy device tunnel 9000/udp --device <hostname>   # against a real UDP service on the device
```
Expected: matches Task 8 Step 8 and Task 9 Step 7's individual validations, now against the fully merged tree.

(No commit — this task makes no file changes. If Steps 1-4 surface issues, fix them in follow-up commits before considering the plan complete.)
