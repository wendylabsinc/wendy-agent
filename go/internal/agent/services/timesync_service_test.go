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
	svc, applied := newTestTimeSyncService(now, proof, nil)

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
	if !applied.IsZero() {
		t.Fatalf("apply must not be called when proof is behind; got %v", *applied)
	}
}

func TestSyncClockRejectsInvalidProof(t *testing.T) {
	svc, _ := newTestTimeSyncService(time.Unix(0, 0), time.Time{}, errors.New("verify: bad signature"))
	_, err := svc.SyncClock(context.Background(), &agentpbv2.SyncClockRequest{Proof: []byte("bad")})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SyncClock err code = %v, want InvalidArgument", status.Code(err))
	}
}
