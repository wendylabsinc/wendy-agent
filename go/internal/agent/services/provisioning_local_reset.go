package services

import (
	"context"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/unenrollproof"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// localResetState is authority only inside the protected provisioning.json
// written after an authenticated operator requested exact local credential
// erasure. It carries no claim about Cloud deletion or certificate revocation.
type localResetState struct {
	Version     int    `json:"version"`
	Principal   string `json:"principal"`
	Fingerprint string `json:"fingerprint"`
}

func validateLocalResetState(state *provisioningState) error {
	record := state.LocalReset
	if record == nil || record.Version != 1 || state.Unenrollment != nil {
		return fmt.Errorf("invalid local reset authorization")
	}
	identity, err := certs.ParsePrincipal(record.Principal)
	if err != nil || identity.EntityType != certs.EntityAsset || !state.Enrolled || state.PrincipalURI != record.Principal || state.KeyPEM != "" {
		return fmt.Errorf("local reset authorization disagrees with enrollment state")
	}
	leaves, err := certs.ParseCertsFromPEM([]byte(state.CertPEM))
	if err != nil || len(leaves) == 0 || unenrollproof.Fingerprint(leaves[0]) != record.Fingerprint {
		return fmt.Errorf("local reset authorization certificate changed")
	}
	return nil
}

func (s *ProvisioningService) finishLocalReset() error {
	state, err := s.readProvisioningState()
	if err != nil {
		return err
	}
	if err := validateLocalResetState(state); err != nil {
		return err
	}
	// Keep the protected pending record until every credential removal is
	// durable. Startup can resume after a partial cleanup without network I/O.
	if err := syncResetDirectory(s.configPath); err != nil {
		return err
	}
	if err := s.clearCredentialFiles(); err != nil {
		return err
	}
	if err := syncResetDirectory(s.configPath); err != nil {
		return err
	}
	return s.saveState(&provisioningState{})
}

func (s *ProvisioningServiceV2) localUnprovision(ctx context.Context, req *agentpbv2.UnprovisionRequest) (*agentpbv2.UnprovisionResponse, error) {
	svc := s.v1
	svc.unenrollmentMu.Lock()
	defer svc.unenrollmentMu.Unlock()
	svc.mu.Lock()
	locked := true
	defer func() {
		if locked {
			svc.mu.Unlock()
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if !supportsTrustedResetStorage {
		return nil, status.Error(codes.FailedPrecondition, "protected local reset storage unavailable; keys retained")
	}
	if err := svc.requirePKIOperator(ctx); err != nil {
		return nil, err
	}
	if !svc.enrolled || req.GetExpectedPrincipalUri() != svc.principalURI || req.GetExpectedCertificateSha256() == "" || req.GetExpectedCertificateSha256() != svc.certificateFingerprintLocked() {
		return nil, status.Error(codes.FailedPrecondition, "exact installed identity required for local reset; keys retained")
	}
	if len(req.GetRevocationProof()) != 0 || len(req.GetCloudDeletion()) != 0 || len(req.GetCloudResetAuthorization()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "local-only reset must not include Cloud unenrollment evidence")
	}
	state, err := svc.readProvisioningState()
	if err != nil {
		return nil, status.Error(codes.Internal, "reading local reset state failed; keys retained")
	}
	if state.Unenrollment != nil {
		return nil, status.Error(codes.FailedPrecondition, "Cloud reset is already pending; resume that operation")
	}
	state.KeyPEM = ""
	state.LocalReset = &localResetState{Version: 1, Principal: svc.principalURI, Fingerprint: req.GetExpectedCertificateSha256()}
	if err := validateLocalResetState(state); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "stored identity changed before local reset; keys retained")
	}
	if err := svc.saveState(state); err != nil {
		return nil, status.Error(codes.Internal, "durable local reset authorization failed; keys retained")
	}
	caller, _ := peer.FromContext(ctx)
	operator, _ := certs.TenantPrincipalFromCert(caller.AuthInfo.(credentials.TLSInfo).State.PeerCertificates[0])
	svc.logger.Info("Operator requested local enrollment reset", zap.String("principal", svc.principalURI), zap.String("operator", operator))
	if err := svc.finishLocalReset(); err != nil {
		svc.logger.Error("Local enrollment cleanup incomplete; recovery will resume it", zap.Error(err))
		return nil, status.Error(codes.Internal, "local credential cleanup incomplete; retry or restart the agent to resume")
	}
	before, cb := svc.clearEnrollmentLocked()
	locked = false
	svc.mu.Unlock()
	notifyUnprovisioned(before, cb)
	return &agentpbv2.UnprovisionResponse{LocalResetCompleted: true}, nil
}
