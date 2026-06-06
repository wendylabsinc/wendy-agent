package services

import (
	"context"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	maxAppGroupServices = 32
	maxEnvEntries       = 256
	maxEnvKeyBytes      = 256
	maxEnvValueBytes    = 4096
	maxUserArgs         = 64
	maxArgBytes         = 4096
	maxCmdBytes         = 4096
	maxAppConfigBytes   = 64 * 1024 // 64 KB
)

// shellMetaRe matches shell metacharacters that must not appear in cmd.
var shellMetaRe = regexp.MustCompile("[;&|`$<>\\\\ ]")

// CreateAppGroup validates the request and returns codes.Unimplemented.
// Access control follows the same model as all other container service RPCs:
// callers must present a valid mTLS client certificate, enforced at the TLS
// handshake layer. The full container-orchestration implementation will be
// added in a follow-up PR.
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

// StopAppGroup validates the request and returns codes.Unimplemented.
// Access control follows the same model as all other container service RPCs:
// callers must present a valid mTLS client certificate, enforced at the TLS
// handshake layer. The full implementation will be added in a follow-up PR.
func (s *ContainerService) StopAppGroup(_ context.Context, req *agentpb.StopAppGroupRequest) (*agentpb.StopAppGroupResponse, error) {
	if req.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	return nil, status.Error(codes.Unimplemented, "StopAppGroup not yet implemented")
}

// validateServiceConfig enforces input limits on a ServiceConfig to prevent
// DoS and injection attacks.
func validateServiceConfig(svc *agentpb.ServiceConfig) error {
	name := svc.GetServiceName()

	// Validate app_config: size cap and JSON schema.
	if len(svc.GetAppConfig()) > maxAppConfigBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: app_config exceeds maximum of %d bytes", name, maxAppConfigBytes)
	}
	if _, err := parseAppConfig(svc.GetAppConfig()); err != nil {
		return status.Errorf(codes.InvalidArgument, "service %q: invalid app_config: %v", name, err)
	}

	// Validate env map: cardinality, key length, and value length.
	if len(svc.GetEnv()) > maxEnvEntries {
		return status.Errorf(codes.InvalidArgument, "service %q: env map exceeds maximum of %d entries", name, maxEnvEntries)
	}
	for k, v := range svc.GetEnv() {
		if len(k) > maxEnvKeyBytes {
			return status.Errorf(codes.InvalidArgument, "service %q: env key exceeds maximum of %d bytes", name, maxEnvKeyBytes)
		}
		if len(v) > maxEnvValueBytes {
			return status.Errorf(codes.InvalidArgument, "service %q: env value for key %q exceeds maximum of %d bytes", name, k, maxEnvValueBytes)
		}
	}

	// Validate cmd: length and absence of shell metacharacters to prevent injection.
	if len(svc.GetCmd()) > maxCmdBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd exceeds maximum of %d bytes", name, maxCmdBytes)
	}
	if svc.GetCmd() != "" && shellMetaRe.MatchString(svc.GetCmd()) {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd contains disallowed shell metacharacters", name)
	}

	// Validate user_args: cardinality and per-entry length.
	if len(svc.GetUserArgs()) > maxUserArgs {
		return status.Errorf(codes.InvalidArgument, "service %q: user_args exceeds maximum of %d entries", name, maxUserArgs)
	}
	for i, arg := range svc.GetUserArgs() {
		if len(arg) > maxArgBytes {
			return status.Errorf(codes.InvalidArgument, "service %q: user_args[%d] exceeds maximum of %d bytes", name, i, maxArgBytes)
		}
	}

	return nil
}
