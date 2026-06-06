package services

import (
	"context"
	"regexp"
	"strconv"
	"strings"

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

// cmdAllowRe restricts cmd to safe, path-like characters only — an allowlist is
// safer than a blocklist for command names because it eliminates entire character
// classes (shell metacharacters, whitespace, control bytes) rather than enumerating
// individual dangerous ones.
var cmdAllowRe = regexp.MustCompile(`^[A-Za-z0-9./_-]+$`)

// imageNameRe restricts image_name to valid OCI image reference characters,
// preventing path traversal sequences, null bytes, and other injection vectors
// that could be exploited when the image reference is passed to a container runtime.
var imageNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._\-/]*[a-z0-9])?(:[a-zA-Z0-9._\-]+)?(@sha256:[a-f0-9]{64})?$`)

// certOrgID extracts the org_id from the caller's mTLS client certificate
// Wendy URI SAN (urn:wendy:org:<org_id>:...). The agent's mTLS transport uses
// tls.RequireAnyClientCert with a custom VerifyPeerCertificate callback
// (see internal/agent/mtls) rather than the standard CA pool path, so
// VerifiedChains is not populated; PeerCertificates[0] holds the verified
// leaf certificate after a successful TLS handshake. Returns Unauthenticated
// when no client cert is present or the connection is not TLS, and
// PermissionDenied when the cert carries no Wendy org identifier.
func certOrgID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "connection is not TLS-authenticated")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", status.Error(codes.Unauthenticated, "no client certificate presented")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	for _, u := range cert.URIs {
		if u.Scheme != "urn" {
			continue
		}
		// Opaque for urn:wendy:org:<org_id>:... is "wendy:org:<org_id>:..."
		parts := strings.SplitN(u.Opaque, ":", 4)
		if len(parts) >= 3 && parts[0] == "wendy" && parts[1] == "org" && parts[2] != "" {
			return parts[2], nil
		}
	}
	return "", status.Error(codes.PermissionDenied, "certificate does not contain a Wendy org identifier")
}

// CreateAppGroup validates the request and returns codes.Unimplemented.
// The full container-orchestration implementation will be added in a follow-up PR.
func (s *ContainerService) CreateAppGroup(req *agentpb.CreateAppGroupRequest, stream grpc.ServerStreamingServer[agentpb.CreateAppGroupProgressResponse]) error {
	orgID, err := certOrgID(stream.Context())
	if err != nil {
		return err
	}
	// app_id is the org_id of the owning tenant expressed as a decimal string —
	// the same namespace as the org_id in the caller's mTLS URI SAN
	// (urn:wendy:org:<org_id>:...). Validating the format enforces this invariant
	// so the equality check below is a verified tenant-identity comparison, not
	// an opaque string comparison between different identifier spaces.
	if req.GetAppId() == "" {
		return status.Error(codes.InvalidArgument, "app_id is required")
	}
	if _, err := strconv.ParseInt(req.GetAppId(), 10, 64); err != nil {
		return status.Error(codes.InvalidArgument, "app_id must be a numeric org identifier")
	}
	if orgID != req.GetAppId() {
		return status.Error(codes.PermissionDenied, "app_id does not match caller's org identity")
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
	orgID, err := certOrgID(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	if _, err := strconv.ParseInt(req.GetAppId(), 10, 64); err != nil {
		return nil, status.Error(codes.InvalidArgument, "app_id must be a numeric org identifier")
	}
	if orgID != req.GetAppId() {
		return nil, status.Error(codes.PermissionDenied, "app_id does not match caller's org identity")
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

	// Validate image_name: must be a well-formed OCI image reference to prevent
	// path traversal, registry override, and other injection vectors.
	if img := svc.GetImageName(); img != "" && !imageNameRe.MatchString(img) {
		return status.Errorf(codes.InvalidArgument, "service %q: image_name is invalid", name)
	}

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

	// Validate cmd: length and only allow safe path-like characters.
	if len(svc.GetCmd()) > maxCmdBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd exceeds maximum of %d bytes", name, maxCmdBytes)
	}
	if svc.GetCmd() != "" && !cmdAllowRe.MatchString(svc.GetCmd()) {
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
