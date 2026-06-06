package services

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const (
	maxAppGroupServices = 32
	maxDependsOn        = 32
	maxServiceNameBytes = 64
	maxImageNameBytes   = 512
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

// serviceNameRe restricts service_name and depends_on entries to safe identifier
// characters. Service names are used as container labels, cgroup identifiers, and
// orchestration lookup keys; allowing shell metacharacters or path-traversal
// sequences in those fields would create injection vectors at every call site.
var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// imageNameRe restricts image_name to valid OCI image reference characters,
// preventing path traversal sequences, null bytes, and other injection vectors
// that could be exploited when the image reference is passed to a container runtime.
// The optional leading group matches a registry hostname with an optional port
// (e.g. registry.example.com:5000/ or localhost:5000/) so that private registries
// with explicit port numbers are accepted alongside simple name references.
var imageNameRe = regexp.MustCompile(`^([a-z0-9][a-z0-9._\-]*(:[0-9]{1,5})?/)?[a-z0-9]([a-z0-9._\-/]*[a-z0-9])?(:[a-zA-Z0-9._\-]+)?(@sha256:[a-f0-9]{64})?$`)

// certOrgID extracts the org_id from the caller's mTLS client certificate
// Wendy URI SAN (urn:wendy:org:<org_id>:...).
//
// The agent's TLS stack uses tls.RequireAnyClientCert with the custom
// buildVerifyPeerCertificate callback (internal/agent/mtls/mldsa_verify.go).
// That callback performs full chain verification for both standard (RSA/ECDSA
// via x509.Verify with ExtKeyUsageClientAuth) and post-quantum (ML-DSA)
// certificates, rejecting any cert whose chain cannot be verified against the
// provisioned CA. Because the callback replaces Go's built-in chain verifier,
// VerifiedChains is not populated; PeerCertificates[0] is the authenticated
// leaf after a successful handshake.
//
// certOrgID guards on HandshakeComplete before reading PeerCertificates[0].
// HandshakeComplete is set to true only after VerifyPeerCertificate returns
// nil, so this assertion ensures the cert was accepted by the verifier and is
// not merely a presented-but-unverified value.
//
// Returns Unauthenticated when no client cert is present or the connection is
// not TLS, and PermissionDenied when the cert carries no Wendy org identifier.
func certOrgID(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "connection is not TLS-authenticated")
	}
	// HandshakeComplete is set only after VerifyPeerCertificate returns nil,
	// confirming the custom callback accepted the chain. This guard prevents
	// PeerCertificates[0] from being trusted if the handshake is somehow
	// incomplete (e.g., a misconfigured test server or future refactor).
	if !tlsInfo.State.HandshakeComplete {
		return "", status.Error(codes.Unauthenticated, "TLS handshake not complete")
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
		s.logger.Info("CreateAppGroup.authz", zap.String("result", "denied"), zap.String("reason", err.Error()))
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
		s.logger.Info("CreateAppGroup.authz",
			zap.String("org_id", orgID),
			zap.String("app_id", req.GetAppId()),
			zap.String("result", "denied"),
			zap.String("reason", "app_id does not match caller's org identity"),
		)
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
	// Verify that every depends_on entry names a service declared in this request.
	// Dangling references would stall the orchestration layer at runtime; catching
	// them here surfaces the misconfiguration early with a clear error.
	serviceNames := make(map[string]struct{}, len(req.GetServices()))
	for _, svc := range req.GetServices() {
		serviceNames[svc.GetServiceName()] = struct{}{}
	}
	for _, svc := range req.GetServices() {
		for _, dep := range svc.GetDependsOn() {
			if _, ok := serviceNames[dep]; !ok {
				return status.Errorf(codes.InvalidArgument, "service %q: depends_on references unknown service %q", svc.GetServiceName(), dep)
			}
		}
	}
	// Log auth success only after all validation passes so the log entry
	// accurately reflects a fully-validated, accepted request.
	s.logger.Info("CreateAppGroup.authz",
		zap.String("org_id", orgID),
		zap.String("app_id", req.GetAppId()),
		zap.Stringer("isolation", req.GetIsolation()),
		zap.Int("service_count", len(req.GetServices())),
		zap.String("result", "ok"),
	)
	return status.Error(codes.Unimplemented, "CreateAppGroup not yet implemented")
}

// StopAppGroup validates the request and returns codes.Unimplemented.
// The full implementation will be added in a follow-up PR.
func (s *ContainerService) StopAppGroup(ctx context.Context, req *agentpb.StopAppGroupRequest) (*agentpb.StopAppGroupResponse, error) {
	orgID, err := certOrgID(ctx)
	if err != nil {
		s.logger.Info("StopAppGroup.authz", zap.String("result", "denied"), zap.String("reason", err.Error()))
		return nil, err
	}
	if req.GetAppId() == "" {
		return nil, status.Error(codes.InvalidArgument, "app_id is required")
	}
	if _, err := strconv.ParseInt(req.GetAppId(), 10, 64); err != nil {
		return nil, status.Error(codes.InvalidArgument, "app_id must be a numeric org identifier")
	}
	if orgID != req.GetAppId() {
		s.logger.Info("StopAppGroup.authz",
			zap.String("org_id", orgID),
			zap.String("app_id", req.GetAppId()),
			zap.String("result", "denied"),
			zap.String("reason", "app_id does not match caller's org identity"),
		)
		return nil, status.Error(codes.PermissionDenied, "app_id does not match caller's org identity")
	}
	s.logger.Info("StopAppGroup.authz",
		zap.String("org_id", orgID),
		zap.String("app_id", req.GetAppId()),
		zap.String("result", "ok"),
	)
	return nil, status.Error(codes.Unimplemented, "StopAppGroup not yet implemented")
}

// validateIsolationMode rejects unspecified and unknown enum values. Callers must
// explicitly choose an isolation level; silently defaulting to the zero value
// would mask misconfigured requests.
func validateIsolationMode(mode agentpb.IsolationMode) error {
	switch mode {
	case agentpb.IsolationMode_ISOLATION_MODE_ISOLATED,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_NETWORK,
		agentpb.IsolationMode_ISOLATION_MODE_SHARED_IPC:
		return nil
	case agentpb.IsolationMode_ISOLATION_MODE_UNSPECIFIED:
		return status.Error(codes.InvalidArgument, "isolation mode must be specified explicitly")
	default:
		return status.Errorf(codes.InvalidArgument, "unsupported isolation mode: %v", mode)
	}
}

// validateServiceConfig enforces input limits on a ServiceConfig to prevent
// DoS and injection attacks.
func validateServiceConfig(svc *agentpb.ServiceConfig) error {
	name := svc.GetServiceName()

	// Require service_name so all error messages and orchestration references are actionable.
	if name == "" {
		return status.Error(codes.InvalidArgument, "service_name is required")
	}
	if len(name) > maxServiceNameBytes {
		return status.Errorf(codes.InvalidArgument, "service_name exceeds maximum of %d bytes", maxServiceNameBytes)
	}
	if !serviceNameRe.MatchString(name) {
		return status.Error(codes.InvalidArgument, "service_name contains disallowed characters")
	}

	// Require image_name so the orchestration layer can pull the container image.
	if svc.GetImageName() == "" {
		return status.Errorf(codes.InvalidArgument, "service %q: image_name is required", name)
	}
	if len(svc.GetImageName()) > maxImageNameBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: image_name exceeds maximum of %d bytes", name, maxImageNameBytes)
	}

	// Validate image_name: must be a well-formed OCI image reference to prevent
	// path traversal, registry override, and other injection vectors.
	if !imageNameRe.MatchString(svc.GetImageName()) {
		return status.Errorf(codes.InvalidArgument, "service %q: image_name is invalid", name)
	}

	// Validate depends_on: cap cardinality and apply the same content allowlist as
	// service_name — entries are used as orchestration lookup keys, so they must
	// identify a valid service_name and cannot contain injection characters.
	if len(svc.GetDependsOn()) > maxDependsOn {
		return status.Errorf(codes.InvalidArgument, "service %q: depends_on exceeds maximum of %d entries", name, maxDependsOn)
	}
	for i, dep := range svc.GetDependsOn() {
		if !serviceNameRe.MatchString(dep) {
			return status.Errorf(codes.InvalidArgument, "service %q: depends_on[%d] contains disallowed characters", name, i)
		}
	}

	// Validate app_config: size cap and structural JSON parsing via parseAppConfig
	// (defined in container_service.go). parseAppConfig unmarshals into appconfig.AppConfig
	// and validates AppID format; it returns a non-nil error for malformed JSON or
	// invalid field values.
	if len(svc.GetAppConfig()) > maxAppConfigBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: app_config exceeds maximum of %d bytes", name, maxAppConfigBytes)
	}
	if _, err := parseAppConfig(svc.GetAppConfig()); err != nil {
		return status.Errorf(codes.InvalidArgument, "service %q: invalid app_config", name)
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
		if strings.ContainsAny(v, "\x00\r\n") {
			return status.Errorf(codes.InvalidArgument, "service %q: env value for key %q contains disallowed character", name, k)
		}
	}

	// Validate cmd: length and only allow safe path-like characters.
	if len(svc.GetCmd()) > maxCmdBytes {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd exceeds maximum of %d bytes", name, maxCmdBytes)
	}
	if svc.GetCmd() != "" && !cmdAllowRe.MatchString(svc.GetCmd()) {
		return status.Errorf(codes.InvalidArgument, "service %q: cmd contains disallowed characters", name)
	}

	// Validate user_args: cardinality, per-entry length, and null/control-byte check.
	// Null bytes, carriage returns, and newlines are rejected because they can corrupt
	// argument lists if any layer between here and exec(2) treats the args as a
	// line-oriented or null-terminated string rather than a []string slice.
	if len(svc.GetUserArgs()) > maxUserArgs {
		return status.Errorf(codes.InvalidArgument, "service %q: user_args exceeds maximum of %d entries", name, maxUserArgs)
	}
	for i, arg := range svc.GetUserArgs() {
		if len(arg) > maxArgBytes {
			return status.Errorf(codes.InvalidArgument, "service %q: user_args[%d] exceeds maximum of %d bytes", name, i, maxArgBytes)
		}
		if strings.ContainsAny(arg, "\x00\r\n") {
			return status.Errorf(codes.InvalidArgument, "service %q: user_args[%d] contains disallowed characters", name, i)
		}
	}

	return nil
}
