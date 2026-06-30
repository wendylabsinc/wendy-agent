package services

import (
	"context"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// GetOSUpdateStatus reports the persisted outcome of the most recent OS
// update attempt (see oshealth.Gate). The record survives an A/B rollback
// because it lives on the data partition.
func (s *AgentService) GetOSUpdateStatus(_ context.Context, _ *agentpb.GetOSUpdateStatusRequest) (*agentpb.GetOSUpdateStatusResponse, error) {
	record, found, err := oshealth.ReadUpdateResult(s.osUpdateStateDir)
	if err != nil {
		s.logger.Warn("Failed to read OS update result record", zap.Error(err))
	}
	if !found || err != nil {
		return &agentpb.GetOSUpdateStatusResponse{HasResult: false}, nil
	}
	return osUpdateStatusToProtoV1(record), nil
}

func osUpdateStatusToProtoV1(r oshealth.UpdateResult) *agentpb.GetOSUpdateStatusResponse {
	resp := &agentpb.GetOSUpdateStatusResponse{
		HasResult:     true,
		Outcome:       outcomeToProtoV1(r.Outcome),
		OldOsVersion:  r.OldOSVersion,
		NewOsVersion:  r.NewOSVersion,
		CreatedAtUnix: r.CreatedAt.Unix(),
		RollbackError: r.RollbackError,
		Note:          r.Note,
	}
	if !r.FinalizedAt.IsZero() {
		resp.FinalizedAtUnix = r.FinalizedAt.Unix()
	}
	for _, svc := range r.Services {
		resp.Services = append(resp.Services, &agentpb.GetOSUpdateStatusResponse_ServiceResult{
			Unit:   svc.Unit,
			Status: serviceStatusToProtoV1(svc.Status),
			Reason: svc.Reason,
		})
	}
	return resp
}

func outcomeToProtoV1(o oshealth.Outcome) agentpb.GetOSUpdateStatusResponse_Outcome {
	switch o {
	case oshealth.OutcomeCommitted:
		return agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMITTED
	case oshealth.OutcomeRolledBack:
		return agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLED_BACK
	case oshealth.OutcomeRollbackFailed:
		return agentpb.GetOSUpdateStatusResponse_OUTCOME_ROLLBACK_FAILED
	case oshealth.OutcomeCommitFailed:
		return agentpb.GetOSUpdateStatusResponse_OUTCOME_COMMIT_FAILED
	default:
		return agentpb.GetOSUpdateStatusResponse_OUTCOME_UNSPECIFIED
	}
}

func serviceStatusToProtoV1(s oshealth.ServiceStatus) agentpb.GetOSUpdateStatusResponse_ServiceResult_Status {
	switch s {
	case oshealth.StatusHealthy:
		return agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_HEALTHY
	case oshealth.StatusSkipped:
		return agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_SKIPPED
	case oshealth.StatusFailed:
		return agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_FAILED
	default:
		return agentpb.GetOSUpdateStatusResponse_ServiceResult_STATUS_UNSPECIFIED
	}
}

// GetOSUpdateStatus is the v2 mirror of AgentService.GetOSUpdateStatus.
func (s *OSUpdateService) GetOSUpdateStatus(_ context.Context, _ *agentpbv2.GetOSUpdateStatusRequest) (*agentpbv2.GetOSUpdateStatusResponse, error) {
	record, found, err := oshealth.ReadUpdateResult(s.stateDir)
	if err != nil {
		s.logger.Warn("Failed to read OS update result record", zap.Error(err))
	}
	if !found || err != nil {
		return &agentpbv2.GetOSUpdateStatusResponse{HasResult: false}, nil
	}
	return osUpdateStatusToProtoV2(record), nil
}

func osUpdateStatusToProtoV2(r oshealth.UpdateResult) *agentpbv2.GetOSUpdateStatusResponse {
	v1 := osUpdateStatusToProtoV1(r)
	resp := &agentpbv2.GetOSUpdateStatusResponse{
		HasResult:       v1.HasResult,
		Outcome:         agentpbv2.GetOSUpdateStatusResponse_Outcome(v1.Outcome),
		OldOsVersion:    v1.OldOsVersion,
		NewOsVersion:    v1.NewOsVersion,
		CreatedAtUnix:   v1.CreatedAtUnix,
		FinalizedAtUnix: v1.FinalizedAtUnix,
		RollbackError:   v1.RollbackError,
		Note:            v1.Note,
	}
	for _, svc := range v1.Services {
		resp.Services = append(resp.Services, &agentpbv2.GetOSUpdateStatusResponse_ServiceResult{
			Unit:   svc.Unit,
			Status: agentpbv2.GetOSUpdateStatusResponse_ServiceResult_Status(svc.Status),
			Reason: svc.Reason,
		})
	}
	return resp
}
