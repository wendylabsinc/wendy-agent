package services

import (
	"context"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
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

// shellMetaRe matches characters that must not appear in cmd to prevent injection.
var shellMetaRe = regexp.MustCompile(`[;&|` + "`" + `$<>\\ \x00]`)

// requireVerifiedClientCert returns an error if the RPC was not made over a
// mutually-authenticated TLS connection with a verified client certificate chain.
// The mTLS TLS config (see internal/agent/mtls) already enforces
// tls.RequireAndVerifyClientCert; this check provides an explicit in-handler
// guard so that authorization failures are visible in code, not just in config.
func requireVerifiedClientCert(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return status.Error(codes.Unauthenticated, "connection is not TLS-authenticated")
	}
	if len(tlsInfo.State.VerifiedChains) == 0 || len(tlsInfo.State.VerifiedChains[0]) == 0 {
		return status.Error(codes.Unauthenticated, "no verified client certificate chain")
	}
	return nil
}

// CreateAppGroup validates the request and returns codes.Unimplemented.
// The full container-orchestration implementation will be added in a follow-up PR.
func (s *ContainerService) CreateAppGroup(req *agentpb.CreateAppGroupRequest, stream grpc.ServerStreamingServer[agentpb.CreateAppGroupProgressResponse]) error {
	if err := requireVerifiedClientCert(stream.Context()); err != nil {
		return err
	}
	if req.GetAppId() == "" {
		return status.Error(codes.InvalidArgument, "app_id is required")
	}
	if err := validateIsolationMode(req.GetIsolation()); err != nil {
		return err
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
// The full implementation will be added in a follow-up PR.
func (s *ContainerService) StopAppGroup(ctx context.Context, req *agentpb.StopAppGroupRequest) (*agentpb.StopAppGroupResponse, error) {
	if err := requireVerifiedClientCert(ctx); err != nil {
		return nil, err
	}
	if req.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	return nil, status.Error(codes.Unimplemented, "StopAppGroup not yet implemented")
}

// validateIsolationMode rejects unknown enum values to prevent unsafe fallback behaviour.
func validateIsolationMode(mode agentpb.IsolationMode) error {
	switch mode {
	case agentpb.IsolationMode_ISOLATION_MODE_UNSPECIFIED,
		agentpb.IsolationMode_ISOLATION_MODE_ISOLATED,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_NETWORK,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_IPC:
		return nil
	default:
		return status.Errorf(codes.InvalidArgument, "unsupported isolation mode: %v", mode)
	}
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

	// Validate env map: cardinality, key content, key length, and value length.
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
		// Reject keys containing '=' or null bytes to prevent env-var injection.
		for _, c := range k {
			if c == '=' || c == 0 {
				return status.Errorf(codes.InvalidArgument, "service %q: env key %q contains disallowed character", name, k)
			}
		}
	}

	// Validate cmd: length and absence of shell metacharacters to prevent injection.
	if len(svc.GetCmd()) > maxCmdBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd exceeds maximum of %d bytes", name, maxCmdBytes)
	}
	if svc.GetCmd() != "" && shellMetaRe.MatchString(svc.GetCmd()) {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd contains disallowed characters", name)
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
