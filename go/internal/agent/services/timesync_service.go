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
		process: timesync.SafeProcessPacket,
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
