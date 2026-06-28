# Device Clock Auto-Correct Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the CLI connects to a WendyOS device, detect a lagging device clock and relay a cryptographically-verified Roughtime proof so the device advances its own clock — fixing the 1970-epoch timestamps in issue #1171.

**Architecture:** A new agent v2 gRPC service `WendyTimeSyncService` exposes `GetClock` (read) and `SyncClock` (apply a Roughtime proof, verified on-device via the existing `timesync.ProcessMulticastPacket`). The CLI reads the device clock during the shared `resolveTarget` path; if it lags the host clock by more than a threshold, it fetches one signed proof and relays it unicast. The host wall clock is never sent as authoritative — only used to decide *whether* to relay.

**Tech Stack:** Go, gRPC / protobuf (`protoc` + `protoc-gen-go` + `protoc-gen-go-grpc`), Roughtime, `go.uber.org/zap`, stdlib `testing`.

## Global Constraints

- Generated protobuf code lives in `go/proto/gen/` and **must not be edited by hand** — regenerate with `cd go && make proto`.
- New `.proto` files must be added to the relevant array in `go/scripts/generate-proto.sh` or they won't be generated.
- Proto package for v2 services: `wendy.agent.services.v2`, `option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2"`.
- The device clock is only ever **advanced**, never moved backward (`timesync.AdvanceTo` enforces this).
- The host's wall clock is **never** transmitted as time; corrections are always a verified Roughtime midpoint.
- All clock-fix work is **best-effort**: no failure path may ever fail or block a user command beyond `clockFixTimeout`.
- Tests are stdlib `testing` only (no testify) — match existing files in `go/internal/agent/services/`.
- Commit messages end with the repo trailer block:
  ```
  Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
  ```

---

### Task 1: Proto definition + generated stubs

**Files:**
- Create: `Proto/wendy/agent/services/v2/timesync_service.proto`
- Modify: `go/scripts/generate-proto.sh` (add to `V2_AGENT_PROTOS` array, ~line 39-50)
- Generated (do not hand-edit): `go/proto/gen/agentpb/v2/timesync_service.pb.go`, `go/proto/gen/agentpb/v2/timesync_service_grpc.pb.go`

**Interfaces:**
- Produces (used by all later tasks):
  - `agentpbv2.WendyTimeSyncServiceServer` interface with `GetClock(context.Context, *GetClockRequest) (*GetClockResponse, error)` and `SyncClock(context.Context, *SyncClockRequest) (*SyncClockResponse, error)`
  - `agentpbv2.UnimplementedWendyTimeSyncServiceServer`
  - `agentpbv2.RegisterWendyTimeSyncServiceServer(grpc.ServiceRegistrar, WendyTimeSyncServiceServer)`
  - `agentpbv2.WendyTimeSyncServiceClient` with the same two methods (client signatures take `...grpc.CallOption`)
  - `agentpbv2.NewWendyTimeSyncServiceClient(grpc.ClientConnInterface) WendyTimeSyncServiceClient`
  - Messages: `GetClockRequest{}`, `GetClockResponse{ UnixNanos int64 }` (getter `GetUnixNanos()`), `SyncClockRequest{ Proof []byte }` (getter `GetProof()`), `SyncClockResponse{ BeforeUnixNanos int64; AfterUnixNanos int64; Applied bool }` (getters `GetBeforeUnixNanos()`, `GetAfterUnixNanos()`, `GetApplied()`)

- [ ] **Step 1: Write the proto file**

Create `Proto/wendy/agent/services/v2/timesync_service.proto`:

```proto
syntax = "proto3";
package wendy.agent.services.v2;
option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";

// WendyTimeSyncService lets a well-connected host correct a device whose wall
// clock is unset or lagging. The host never sends its own clock as
// authoritative time; it relays a cryptographically-signed Roughtime proof
// that the device verifies itself before advancing (never backward).
service WendyTimeSyncService {
    // GetClock returns the device's current wall clock. Cheap; used by the host
    // to decide whether a correction is needed.
    rpc GetClock(GetClockRequest) returns (GetClockResponse);

    // SyncClock relays a Roughtime proof. The device verifies the signature and
    // advances its clock if the proof's time is ahead of the current time.
    rpc SyncClock(SyncClockRequest) returns (SyncClockResponse);
}

message GetClockRequest {}

message GetClockResponse {
    // Device time at handling, as Unix nanoseconds.
    int64 unix_nanos = 1;
}

message SyncClockRequest {
    // A WendyDatagram packet carrying a Roughtime proof — byte-identical to the
    // multicast relay format (roughtime.Encode of a MsgTypeRoughtime datagram).
    bytes proof = 1;
}

message SyncClockResponse {
    // Device clock before applying, as Unix nanoseconds.
    int64 before_unix_nanos = 1;
    // Device clock after applying, as Unix nanoseconds.
    int64 after_unix_nanos = 2;
    // True if the clock was actually advanced.
    bool applied = 3;
}
```

- [ ] **Step 2: Register the proto in the generation script**

In `go/scripts/generate-proto.sh`, add the new path to the `V2_AGENT_PROTOS=( ... )` array (after `"wendy/agent/services/v2/ros2_service.proto"`):

```bash
    "wendy/agent/services/v2/ros2_service.proto"
    "wendy/agent/services/v2/timesync_service.proto"
)
```

- [ ] **Step 3: Generate the stubs**

Run (from repo root):

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
cd go && make proto
```

Expected: completes without error; prints "Generating Wendy Agent v2 protos...".

> If `protoc` is not installed, install it (`brew install protobuf` on macOS) before re-running. Do not hand-write the generated files.

- [ ] **Step 4: Verify generated files exist and compile**

Run:

```bash
ls go/proto/gen/agentpb/v2/timesync_service.pb.go go/proto/gen/agentpb/v2/timesync_service_grpc.pb.go
cd go && go build ./proto/...
```

Expected: both files listed; build succeeds.

- [ ] **Step 5: Commit**

```bash
git add Proto/wendy/agent/services/v2/timesync_service.proto go/scripts/generate-proto.sh go/proto/gen/agentpb/v2/
git commit -m "$(cat <<'EOF'
feat(proto): add WendyTimeSyncService (GetClock/SyncClock)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 2: Agent-side TimeSyncService handler

**Files:**
- Create: `go/internal/agent/services/timesync_service.go`
- Test: `go/internal/agent/services/timesync_service_test.go`

**Interfaces:**
- Consumes (from Task 1): `agentpbv2.UnimplementedWendyTimeSyncServiceServer`, request/response messages.
- Consumes (existing): `timesync.ProcessMulticastPacket(pkt []byte) (time.Time, error)`; `(*timesync.Manager).Apply(t time.Time)`.
- Produces (used by Task 3): `services.NewTimeSyncService(logger *zap.Logger, mgr *timesync.Manager) *TimeSyncService` — implements `agentpbv2.WendyTimeSyncServiceServer`.

**Design note:** The handler uses three injectable seams so unit tests never touch the real system clock:
- `now func() time.Time` (default `time.Now`)
- `process func([]byte) (time.Time, error)` (default `timesync.ProcessMulticastPacket`)
- `apply func(time.Time)` (default `mgr.Apply`)

`after`/`applied` are computed logically (`applied = midpoint.After(before)`, `after = max(before, midpoint)`) to mirror `AdvanceTo`'s forward-only semantics without re-reading the clock.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/services/timesync_service_test.go`:

```go
package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestTimeSyncService(now time.Time, proofTime time.Time, proofErr error) (*TimeSyncService, *time.Time) {
	var applied time.Time
	svc := NewTimeSyncService(zap.NewNop(), nil)
	svc.now = func() time.Time { return now }
	svc.process = func([]byte) (time.Time, error) { return proofTime, proofErr }
	svc.apply = func(t time.Time) { applied = t }
	return svc, &applied
}

func TestGetClockReturnsNow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	svc, _ := newTestTimeSyncService(now, time.Time{}, nil)
	resp, err := svc.GetClock(context.Background(), &agentpbv2.GetClockRequest{})
	if err != nil {
		t.Fatalf("GetClock error: %v", err)
	}
	if resp.GetUnixNanos() != now.UnixNano() {
		t.Fatalf("GetClock = %d, want %d", resp.GetUnixNanos(), now.UnixNano())
	}
}

func TestSyncClockAdvancesWhenProofAhead(t *testing.T) {
	now := time.Unix(0, 0)               // device thinks it's 1970
	proof := time.Unix(1_700_000_000, 0) // verified midpoint
	svc, applied := newTestTimeSyncService(now, proof, nil)

	resp, err := svc.SyncClock(context.Background(), &agentpbv2.SyncClockRequest{Proof: []byte("x")})
	if err != nil {
		t.Fatalf("SyncClock error: %v", err)
	}
	if !resp.GetApplied() {
		t.Fatal("expected Applied=true")
	}
	if resp.GetBeforeUnixNanos() != now.UnixNano() || resp.GetAfterUnixNanos() != proof.UnixNano() {
		t.Fatalf("before/after = %d/%d, want %d/%d",
			resp.GetBeforeUnixNanos(), resp.GetAfterUnixNanos(), now.UnixNano(), proof.UnixNano())
	}
	if !applied.Equal(proof) {
		t.Fatalf("apply called with %v, want %v", *applied, proof)
	}
}

func TestSyncClockNoOpWhenProofBehind(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	proof := time.Unix(1_600_000_000, 0) // older than current clock
	svc, _ := newTestTimeSyncService(now, proof, nil)

	resp, err := svc.SyncClock(context.Background(), &agentpbv2.SyncClockRequest{Proof: []byte("x")})
	if err != nil {
		t.Fatalf("SyncClock error: %v", err)
	}
	if resp.GetApplied() {
		t.Fatal("expected Applied=false for a backward proof")
	}
	if resp.GetAfterUnixNanos() != now.UnixNano() {
		t.Fatalf("after = %d, want %d (unchanged)", resp.GetAfterUnixNanos(), now.UnixNano())
	}
}

func TestSyncClockRejectsInvalidProof(t *testing.T) {
	svc, _ := newTestTimeSyncService(time.Unix(0, 0), time.Time{}, errors.New("verify: bad signature"))
	_, err := svc.SyncClock(context.Background(), &agentpbv2.SyncClockRequest{Proof: []byte("bad")})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SyncClock err code = %v, want InvalidArgument", status.Code(err))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd go && go test ./internal/agent/services/ -run 'TestGetClock|TestSyncClock' -v
```

Expected: FAIL — `undefined: NewTimeSyncService` / `TimeSyncService`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/agent/services/timesync_service.go`:

```go
package services

import (
	"context"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// TimeSyncService serves the device clock and applies host-relayed Roughtime
// proofs. The host never sends authoritative time; SyncClock verifies the
// signed proof on-device (reusing the multicast-relay verification) and only
// ever advances the clock.
type TimeSyncService struct {
	agentpbv2.UnimplementedWendyTimeSyncServiceServer
	logger *zap.Logger

	// Seams (overridden in tests).
	now     func() time.Time
	process func([]byte) (time.Time, error)
	apply   func(time.Time)
}

// NewTimeSyncService builds the service. mgr supplies the real clock-advance
// path; it may be nil in tests that override the apply seam.
func NewTimeSyncService(logger *zap.Logger, mgr *timesync.Manager) *TimeSyncService {
	apply := func(time.Time) {}
	if mgr != nil {
		apply = mgr.Apply
	}
	return &TimeSyncService{
		logger:  logger,
		now:     time.Now,
		process: timesync.ProcessMulticastPacket,
		apply:   apply,
	}
}

func (s *TimeSyncService) GetClock(_ context.Context, _ *agentpbv2.GetClockRequest) (*agentpbv2.GetClockResponse, error) {
	return &agentpbv2.GetClockResponse{UnixNanos: s.now().UnixNano()}, nil
}

func (s *TimeSyncService) SyncClock(_ context.Context, req *agentpbv2.SyncClockRequest) (*agentpbv2.SyncClockResponse, error) {
	t, err := s.process(req.GetProof())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid time proof: %v", err)
	}

	before := s.now()
	// Zero time means an unknown msg_type (forward-compat) — nothing to apply.
	applied := !t.IsZero() && t.After(before)
	after := before
	if applied {
		s.apply(t)
		after = t
		if s.logger != nil {
			s.logger.Info("timesync: clock advanced via host relay",
				zap.Time("before", before), zap.Time("after", after))
		}
	}
	return &agentpbv2.SyncClockResponse{
		BeforeUnixNanos: before.UnixNano(),
		AfterUnixNanos:  after.UnixNano(),
		Applied:         applied,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
cd go && go test ./internal/agent/services/ -run 'TestGetClock|TestSyncClock' -v
```

Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/timesync_service.go go/internal/agent/services/timesync_service_test.go
git commit -m "$(cat <<'EOF'
feat(agent): TimeSyncService GetClock/SyncClock handler

SyncClock verifies the relayed Roughtime proof on-device and only ever
advances the clock. Host wall time is never trusted as authoritative.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 3: Register the service in the agent

**Files:**
- Modify: `go/cmd/wendy-agent/main.go` (construct near line 193 with the other `services.New*`; register near line 399 with the other `agentpbv2.Register*`)

**Interfaces:**
- Consumes: `services.NewTimeSyncService` (Task 2); existing `timesyncMgr` (`main.go:128`); existing `logger`.

- [ ] **Step 1: Construct the service**

In `go/cmd/wendy-agent/main.go`, after the line `deviceInfoSvc := services.NewDeviceInfoService(logger, hwDiscoverer)` (~line 193), add:

```go
	timeSyncSvc := services.NewTimeSyncService(logger, timesyncMgr)
```

- [ ] **Step 2: Register the service**

In the `registerAllServices` closure, after `agentpbv2.RegisterWendyDeviceInfoServiceServer(srv, deviceInfoSvc)` (~line 399), add:

```go
	agentpbv2.RegisterWendyTimeSyncServiceServer(srv, timeSyncSvc)
```

- [ ] **Step 3: Build the agent to verify wiring**

Run:

```bash
cd go && go build ./cmd/wendy-agent
```

Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add go/cmd/wendy-agent/main.go
git commit -m "$(cat <<'EOF'
feat(agent): register WendyTimeSyncService

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 4: CLI proof fetch helper + memoization

**Files:**
- Modify: `go/internal/cli/timesync/sender.go`
- Test: `go/internal/cli/timesync/sender_test.go` (create)

**Interfaces:**
- Consumes (existing): `roughtime.Query`, `roughtime.Result{ Server string; Nonce, RawResponse []byte }`, `roughtime.EncodeRoughtimePayload`, `roughtime.Encode`, `roughtime.MsgTypeRoughtime`, `timesync.Servers`.
- Produces (used by Task 6): `clitimesync.FetchProofPacket(ctx context.Context) (pkt []byte, result roughtime.Result, err error)` — fetches one signed proof, encoded as a WendyDatagram packet, memoized per process so repeated calls in one CLI run issue at most one Roughtime query.

**Design note:** Extract the fetch+encode half of `BroadcastTime` into `encodeProofPacket(result roughtime.Result) []byte`, then build `FetchProofPacket` (memoized) and rewrite `BroadcastTime` to reuse it.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/timesync/sender_test.go`:

```go
package clitimesync

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

func TestFetchProofPacketMemoizes(t *testing.T) {
	calls := 0
	orig := roughtimeQueryFn
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		calls++
		return roughtime.Result{Server: "test", Nonce: []byte("nonce"), RawResponse: []byte("resp")}, nil
	}
	t.Cleanup(func() { roughtimeQueryFn = orig; resetProofCache() })
	resetProofCache()

	pkt1, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket: %v", err)
	}
	pkt2, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("FetchProofPacket (2): %v", err)
	}
	if calls != 1 {
		t.Fatalf("roughtime query called %d times, want 1 (memoized)", calls)
	}
	if len(pkt1) == 0 || string(pkt1) != string(pkt2) {
		t.Fatal("expected identical non-empty packets across calls")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd go && go test ./internal/cli/timesync/ -run TestFetchProofPacket -v
```

Expected: FAIL — `undefined: roughtimeQueryFn` / `FetchProofPacket` / `resetProofCache`.

- [ ] **Step 3: Refactor sender.go**

Edit `go/internal/cli/timesync/sender.go`. Add imports `context` (already present), `sync`. Replace the body of `BroadcastTime` and add the new helpers:

```go
// roughtimeQueryFn is indirected for tests.
var roughtimeQueryFn = roughtime.Query

var (
	proofMu     sync.Mutex
	proofPkt    []byte
	proofResult roughtime.Result
	proofCached bool
)

// resetProofCache clears the per-process proof cache (test helper).
func resetProofCache() {
	proofMu.Lock()
	defer proofMu.Unlock()
	proofPkt, proofResult, proofCached = nil, roughtime.Result{}, false
}

// FetchProofPacket queries a Roughtime server and returns the encoded
// WendyDatagram packet plus the raw result. The result is memoized for the
// life of the process so fixing several devices in one CLI run issues at most
// one Roughtime query.
func FetchProofPacket(ctx context.Context) ([]byte, roughtime.Result, error) {
	proofMu.Lock()
	defer proofMu.Unlock()
	if proofCached {
		return proofPkt, proofResult, nil
	}
	result, err := roughtimeQueryFn(ctx, timesync.Servers)
	if err != nil {
		return nil, roughtime.Result{}, fmt.Errorf("roughtime query: %w", err)
	}
	proofPkt = encodeProofPacket(result)
	proofResult = result
	proofCached = true
	return proofPkt, proofResult, nil
}

// encodeProofPacket builds the WendyDatagram packet the agent verifies.
func encodeProofPacket(result roughtime.Result) []byte {
	serverIdx := uint8(0)
	for i, s := range timesync.Servers {
		if s.Name == result.Server {
			serverIdx = uint8(i)
			break
		}
	}
	payload := roughtime.EncodeRoughtimePayload(roughtime.RoughtimePayload{
		ServerIndex: serverIdx,
		Nonce:       result.Nonce,
		Response:    result.RawResponse,
	})
	return roughtime.Encode(roughtime.Datagram{
		MsgType: roughtime.MsgTypeRoughtime,
		Payload: payload,
	})
}
```

Then rewrite `BroadcastTime` to reuse `FetchProofPacket`:

```go
// BroadcastTime fetches a Roughtime proof and multicasts it as a WendyDatagram
// on all active network interfaces. Best-effort: interface errors are skipped.
// Returns an error only if all Roughtime servers are unreachable.
func BroadcastTime(ctx context.Context) (roughtime.Result, error) {
	pkt, result, err := FetchProofPacket(ctx)
	if err != nil {
		return roughtime.Result{}, err
	}
	sendMulticast(pkt) // best-effort
	return result, nil
}
```

Ensure the import block includes `"fmt"`, `"sync"`, and `"context"` (and keeps `"net"`, the `roughtime` and `timesync` packages).

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
cd go && go test ./internal/cli/timesync/ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/timesync/sender.go go/internal/cli/timesync/sender_test.go
git commit -m "$(cat <<'EOF'
refactor(cli): extract memoized FetchProofPacket from BroadcastTime

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 5: Wire the TimeSync client into AgentConnection

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (struct ~line 46-66; `newAgentConnection` ~line 203-214)

**Interfaces:**
- Consumes (Task 1): `agentpbv2.NewWendyTimeSyncServiceClient`, `agentpbv2.WendyTimeSyncServiceClient`.
- Produces (used by Task 6): `AgentConnection.TimeSyncService agentpbv2.WendyTimeSyncServiceClient`.

> `client.go` already imports `agentpbv2` (used by other v2 clients). If not, add `agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"`.

- [ ] **Step 1: Add the field to AgentConnection**

In the `AgentConnection` struct (after `FileSyncService` ~line 65), add:

```go
	TimeSyncService     agentpbv2.WendyTimeSyncServiceClient
```

- [ ] **Step 2: Populate it in newAgentConnection**

In `newAgentConnection` (after `FileSyncService: agentpb.NewWendyFileSyncServiceClient(conn),`), add:

```go
		TimeSyncService:     agentpbv2.NewWendyTimeSyncServiceClient(conn),
```

- [ ] **Step 3: Build to verify**

Run:

```bash
cd go && go build ./internal/cli/...
```

Expected: build succeeds.

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/grpcclient/client.go
git commit -m "$(cat <<'EOF'
feat(cli): expose TimeSyncService client on AgentConnection

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 6: maybeFixClock detection-and-fix logic

**Files:**
- Create: `go/internal/cli/commands/device_clock.go`
- Test: `go/internal/cli/commands/device_clock_test.go`

**Interfaces:**
- Consumes: `grpcclient.AgentConnection.TimeSyncService` (Task 5); `clitimesync.FetchProofPacket` (Task 4); `agentpbv2` messages (Task 1); existing package vars `jsonOutput` (bool).
- Produces (used by Task 7): `maybeFixClock(ctx context.Context, conn *grpcclient.AgentConnection)`.

**Design note:** Indirect the proof fetch (`fetchProofPacketFn`) and `time.Now` (`clockNowFn`) for tests. Everything is best-effort; on any error the function returns silently (debug log only when `WENDY_TLS_DEBUG` is set, matching the existing convention).

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/device_clock_test.go`:

```go
package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc"
)

type fakeTimeSyncClient struct {
	clockResp *agentpbv2.GetClockResponse
	clockErr  error
	syncResp  *agentpbv2.SyncClockResponse
	syncErr   error
	syncCalls int
	lastProof []byte
}

func (f *fakeTimeSyncClient) GetClock(_ context.Context, _ *agentpbv2.GetClockRequest, _ ...grpc.CallOption) (*agentpbv2.GetClockResponse, error) {
	return f.clockResp, f.clockErr
}

func (f *fakeTimeSyncClient) SyncClock(_ context.Context, in *agentpbv2.SyncClockRequest, _ ...grpc.CallOption) (*agentpbv2.SyncClockResponse, error) {
	f.syncCalls++
	f.lastProof = in.GetProof()
	return f.syncResp, f.syncErr
}

func withClockTestSeams(t *testing.T, host time.Time, proof []byte, proofErr error) {
	t.Helper()
	origNow, origFetch := clockNowFn, fetchProofPacketFn
	clockNowFn = func() time.Time { return host }
	fetchProofPacketFn = func(context.Context) ([]byte, error) { return proof, proofErr }
	t.Cleanup(func() { clockNowFn, fetchProofPacketFn = origNow, origFetch })
}

func TestMaybeFixClock_RelaysWhenBehind(t *testing.T) {
	host := time.Unix(1_700_000_000, 0)
	withClockTestSeams(t, host, []byte("proof"), nil)
	fake := &fakeTimeSyncClient{
		clockResp: &agentpbv2.GetClockResponse{UnixNanos: time.Unix(0, 0).UnixNano()}, // 1970
		syncResp:  &agentpbv2.SyncClockResponse{Applied: true},
	}
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{TimeSyncService: fake})
	if fake.syncCalls != 1 {
		t.Fatalf("SyncClock calls = %d, want 1", fake.syncCalls)
	}
	if string(fake.lastProof) != "proof" {
		t.Fatalf("proof = %q, want %q", fake.lastProof, "proof")
	}
}

func TestMaybeFixClock_SkipsWhenWithinTolerance(t *testing.T) {
	host := time.Unix(1_700_000_000, 0)
	withClockTestSeams(t, host, []byte("proof"), nil)
	fake := &fakeTimeSyncClient{
		clockResp: &agentpbv2.GetClockResponse{UnixNanos: host.Add(-30 * time.Second).UnixNano()},
	}
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{TimeSyncService: fake})
	if fake.syncCalls != 0 {
		t.Fatalf("SyncClock calls = %d, want 0", fake.syncCalls)
	}
}

func TestMaybeFixClock_SkipsWhenAhead(t *testing.T) {
	host := time.Unix(1_700_000_000, 0)
	withClockTestSeams(t, host, []byte("proof"), nil)
	fake := &fakeTimeSyncClient{
		clockResp: &agentpbv2.GetClockResponse{UnixNanos: host.Add(time.Hour).UnixNano()},
	}
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{TimeSyncService: fake})
	if fake.syncCalls != 0 {
		t.Fatalf("SyncClock calls = %d, want 0", fake.syncCalls)
	}
}

func TestMaybeFixClock_BestEffortOnErrors(t *testing.T) {
	host := time.Unix(1_700_000_000, 0)
	withClockTestSeams(t, host, []byte("proof"), nil)
	// GetClock error: must not panic, must not call SyncClock.
	fake := &fakeTimeSyncClient{clockErr: errors.New("unimplemented")}
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{TimeSyncService: fake})
	if fake.syncCalls != 0 {
		t.Fatalf("SyncClock calls = %d, want 0 on GetClock error", fake.syncCalls)
	}
	// Nil service: must not panic.
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{})
	// Nil conn: must not panic.
	maybeFixClock(context.Background(), nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd go && go test ./internal/cli/commands/ -run TestMaybeFixClock -v
```

Expected: FAIL — `undefined: maybeFixClock` / `clockNowFn` / `fetchProofPacketFn`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/cli/commands/device_clock.go`:

```go
package commands

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	clitimesync "github.com/wendylabsinc/wendy/go/internal/cli/timesync"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// clockSkewThreshold is the minimum amount a device clock must lag the host
// clock before we relay a time proof. Large enough to ignore ordinary drift
// and round-trip noise; small enough to catch any meaningfully-wrong clock,
// including the 1970-epoch case from issue #1171.
const clockSkewThreshold = 2 * time.Minute

// clockFixTimeout bounds the whole detect-and-fix exchange so a flaky link
// never stalls a command.
const clockFixTimeout = 5 * time.Second

// clockNowFn is indirected for tests.
var clockNowFn = time.Now

// fetchProofPacketFn is indirected for tests.
var fetchProofPacketFn = func(ctx context.Context) ([]byte, error) {
	pkt, _, err := clitimesync.FetchProofPacket(ctx)
	return pkt, err
}

// maybeFixClock detects whether the connected device's clock lags the host
// clock by more than clockSkewThreshold and, if so, relays a verified
// Roughtime proof so the device advances its own clock. The host wall clock is
// used only to decide whether to relay — never sent as authoritative time.
//
// Best-effort: every failure path is silent (debug-only) and never affects the
// command. The whole exchange is bounded by clockFixTimeout.
func maybeFixClock(ctx context.Context, conn *grpcclient.AgentConnection) {
	if conn == nil || conn.TimeSyncService == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, clockFixTimeout)
	defer cancel()

	resp, err := conn.TimeSyncService.GetClock(ctx, &agentpbv2.GetClockRequest{})
	if err != nil {
		debugClock("GetClock failed: %v", err)
		return
	}
	skew := clockNowFn().Sub(time.Unix(0, resp.GetUnixNanos()))
	if skew <= clockSkewThreshold {
		return
	}

	pkt, err := fetchProofPacketFn(ctx)
	if err != nil {
		debugClock("fetch time proof failed: %v", err)
		return
	}
	syncResp, err := conn.TimeSyncService.SyncClock(ctx, &agentpbv2.SyncClockRequest{Proof: pkt})
	if err != nil {
		debugClock("SyncClock failed: %v", err)
		return
	}
	if syncResp.GetApplied() && !jsonOutput {
		fmt.Fprintf(os.Stderr, "⏱  Device clock was %s behind — synchronized via Roughtime.\n", formatClockSkew(skew))
	}
}

// debugClock logs only when WENDY_TLS_DEBUG is set, matching autoSyncTimeAndRetry.
func debugClock(format string, args ...any) {
	if os.Getenv("WENDY_TLS_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[clock] "+format+"\n", args...)
	}
}

// formatClockSkew renders a coarse, human-friendly magnitude (e.g. "56y", "3h").
func formatClockSkew(d time.Duration) string {
	switch {
	case d >= 365*24*time.Hour:
		return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return d.Round(time.Minute).String()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run:

```bash
cd go && go test ./internal/cli/commands/ -run TestMaybeFixClock -v
```

Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/device_clock.go go/internal/cli/commands/device_clock_test.go
git commit -m "$(cat <<'EOF'
feat(cli): maybeFixClock relays a time proof when the device lags

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 7: Hook maybeFixClock into the device-resolution path

**Files:**
- Modify: `go/internal/cli/commands/helpers.go` (rename the body of `resolveTarget` ~line 1497 to `resolveTargetInner`; add a thin wrapper)
- Modify: `go/internal/cli/commands/run.go` (cloud fallback ~line 607)

**Interfaces:**
- Consumes: `maybeFixClock` (Task 6); existing `resolveTarget` callers (19 sites across `apps.go`, `build.go`, `device.go`, `hardware.go`, `ros2.go`, `volumes.go`, `run.go`, `wifi.go`, `helpers.go`).

**Design note:** `resolveTarget` is the single funnel every command uses to obtain a device. Wrapping it covers `info`, `run`, `deploy`, `ros2 bag …`, etc. in one place. `resolveRunTarget`'s cloud fallback (run.go) is the only agent connection that bypasses `resolveTarget`, so it gets an explicit call too. (`maybeFixClock` is cheap and self-guards on a nil service, so double exposure is harmless.)

- [ ] **Step 1: Rename the existing function body**

In `go/internal/cli/commands/helpers.go`, rename the existing `func resolveTarget(ctx context.Context, opts ...resolveOption) (*SelectedDevice, error) {` to:

```go
func resolveTargetInner(ctx context.Context, opts ...resolveOption) (*SelectedDevice, error) {
```

(Only the declaration line changes; the body is unchanged.)

- [ ] **Step 2: Add the wrapper**

Immediately above `resolveTargetInner`, add:

```go
// resolveTarget resolves the target device and, for agent connections,
// best-effort corrects a lagging device clock before the caller operates on it
// (issue #1171). This is the single funnel every command uses to obtain a
// device, so the clock fix applies to info, run, deploy, ros2 bag, etc.
func resolveTarget(ctx context.Context, opts ...resolveOption) (*SelectedDevice, error) {
	sel, err := resolveTargetInner(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if sel != nil && sel.Agent != nil {
		maybeFixClock(ctx, sel.Agent)
	}
	return sel, nil
}
```

- [ ] **Step 3: Cover the cloud fallback in run.go**

In `go/internal/cli/commands/run.go`, in `resolveRunTarget`, change the cloud fallback return (~line 607):

```go
	cloudConn, cloudErr := connectToCloudAgent(ctx, "", deviceName, "")
	if cloudErr != nil {
		return nil, err
	}
	maybeFixClock(ctx, cloudConn)
	return &SelectedDevice{Agent: cloudConn}, nil
```

- [ ] **Step 4: Build and run the commands package tests**

Run:

```bash
cd go && go build ./... && go test ./internal/cli/commands/ -run 'TestMaybeFixClock|TestResolve|TestAutoSyncTime' -v
```

Expected: build succeeds; tests PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/helpers.go go/internal/cli/commands/run.go
git commit -m "$(cat <<'EOF'
feat(cli): auto-correct device clock during target resolution

Wires maybeFixClock into resolveTarget (the shared device funnel) and run's
cloud fallback, so info/run/deploy/ros2-bag all advance a lagging device
clock on connect. Fixes #1171.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

### Task 8: Full build, vet, lint, and final verification

**Files:** none (verification only).

- [ ] **Step 1: Build everything**

Run:

```bash
cd go && go build ./...
```

Expected: succeeds.

- [ ] **Step 2: Vet**

Run:

```bash
cd go && go vet ./internal/agent/services/ ./internal/cli/timesync/ ./internal/cli/commands/ ./internal/cli/grpcclient/
```

Expected: no findings.

- [ ] **Step 3: Run the full affected test suites**

Run:

```bash
cd go && go test ./internal/agent/services/ ./internal/cli/timesync/ ./internal/cli/commands/ ./internal/agent/timesync/
```

Expected: PASS.

- [ ] **Step 4: Lint (mirrors CI)**

Run:

```bash
cd go && golangci-lint run ./internal/agent/services/... ./internal/cli/... 2>/dev/null || echo "golangci-lint not installed — skipping (CI will run it)"
```

Expected: no new findings (or skipped if not installed).

- [ ] **Step 5: Final commit (only if lint auto-fixes or doc tweaks were needed)**

If nothing changed, skip. Otherwise:

```bash
git add -A
git commit -m "$(cat <<'EOF'
chore: lint/vet fixups for device clock auto-correct

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01HpNpn31rL2189iBUkf2B6y
EOF
)"
```

---

## Manual verification (optional, on real hardware)

1. On a device with a deliberately-wrong clock: `sudo date -s "1970-01-03"` (or boot a device without NTP).
2. From the host: `wendy device info` (or `wendy device ros2 bag record`).
3. Expect a one-line stderr notice: `⏱  Device clock was 56y behind — synchronized via Roughtime.`
4. `wendy device ros2 bag record` then `wendy device ros2 bag list` — new bag names should carry a `2026…` prefix, not `1970…`.

## Notes on trust model (carried from the design)

- `SyncClock` verifies the relayed proof with the **same** on-device path as the multicast relay (`timesync.ProcessMulticastPacket`), so a forged/garbage proof is rejected with `InvalidArgument` and never moves the clock.
- The host clock is only a heuristic for *whether* to relay; a wrong host clock can never set a wrong device time, and the device never moves backward.
- This complements the existing `autoSyncTimeAndRetry` (which fires on a *failed* clock-skew connection via best-effort multicast); `maybeFixClock` fixes the *connected-but-still-behind* case via reliable unicast.
