package services

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	maxAppGroupServices = 32
	maxEnvEntries       = 256
	maxUserArgs         = 64
	maxAppConfigBytes   = 64 * 1024 // 64 KB
)

// CreateAppGroup validates the request and returns codes.Unimplemented. The full
// implementation (container orchestration, status streaming) will be added in a
// follow-up PR. Transport-layer mTLS authenticates callers; app_id ownership
// validation against the caller's certificate org_id claim MUST be added when the
// handler is fully implemented.
func (s *ContainerService) CreateAppGroup(req *agentpb.CreateAppGroupRequest, _ grpc.ServerStreamingServer[agentpb.CreateAppGroupProgressResponse]) error {
	if req.GetAppId() == "" {
		return status.Error(codes.InvalidArgument, "app_id is required")
	}
	if len(req.GetServices()) > maxAppGroupServices {
		return status.Errorf(codes.InvalidArgument, "services count %d exceeds maximum of %d", len(req.GetServices()), maxAppGroupServices)
	}
	for _, svc := range req.GetServices() {
		if err := validateServiceConfig(svc); err != nil {
			return err
		}
	}
	return status.Error(codes.Unimplemented, "CreateAppGroup not yet implemented")
}

// StopAppGroup validates the request and returns codes.Unimplemented. The full
// implementation will be added in a follow-up PR. Transport-layer mTLS authenticates
// callers; app_id ownership validation against the caller's certificate org_id claim
// MUST be added when the handler is fully implemented.
func (s *ContainerService) StopAppGroup(_ context.Context, req *agentpb.StopAppGroupRequest) (*agentpb.StopAppGroupResponse, error) {
	if req.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	return nil, status.Error(codes.Unimplemented, "StopAppGroup not yet implemented")
}

// validateServiceConfig enforces input limits on a ServiceConfig to guard against
// DoS and injection vectors.
func validateServiceConfig(svc *agentpb.ServiceConfig) error {
	if len(svc.GetAppConfig()) > maxAppConfigBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: app_config exceeds maximum of %d bytes", svc.GetServiceName(), maxAppConfigBytes)
	}
	if _, err := parseAppConfig(svc.GetAppConfig()); err != nil {
		return status.Errorf(codes.InvalidArgument, "service %q: invalid app_config: %v", svc.GetServiceName(), err)
	}
	if len(svc.GetEnv()) > maxEnvEntries {
		return status.Errorf(codes.InvalidArgument, "service %q: env map exceeds maximum of %d entries", svc.GetServiceName(), maxEnvEntries)
	}
	if len(svc.GetUserArgs()) > maxUserArgs {
		return status.Errorf(codes.InvalidArgument, "service %q: user_args exceeds maximum of %d entries", svc.GetServiceName(), maxUserArgs)
	}
	return nil
}
