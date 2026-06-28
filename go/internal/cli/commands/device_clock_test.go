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

func TestMaybeFixClock_BestEffortOnProofFetchError(t *testing.T) {
	host := time.Unix(1_700_000_000, 0)
	withClockTestSeams(t, host, nil, errors.New("no internet"))
	fake := &fakeTimeSyncClient{
		clockResp: &agentpbv2.GetClockResponse{UnixNanos: time.Unix(0, 0).UnixNano()}, // device behind -> would relay
	}
	maybeFixClock(context.Background(), &grpcclient.AgentConnection{TimeSyncService: fake})
	if fake.syncCalls != 0 {
		t.Fatalf("SyncClock calls = %d, want 0 when proof fetch fails", fake.syncCalls)
	}
}
