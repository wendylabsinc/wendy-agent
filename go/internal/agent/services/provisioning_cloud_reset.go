package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/unenrollproof"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validateResetBinding(record *unenrollproof.Completion) error {
	identity, err := certs.ParsePrincipal(record.Principal)
	if err != nil || identity.EntityType != certs.EntityAsset {
		return fmt.Errorf("invalid reset identity")
	}
	if record.Cloud == "" || len(record.CloudDeletion) == 0 || len(record.CloudDeletion) > 4096 {
		return fmt.Errorf("invalid Cloud deletion evidence size/endpoint")
	}
	id, err := uuid.Parse(record.AssetID)
	if err != nil || id.String() != record.AssetID {
		return fmt.Errorf("invalid reset asset UUID")
	}
	var deleted cloudpbv2.DeletedAsset
	if err := proto.Unmarshal(record.CloudDeletion, &deleted); err != nil {
		return err
	}
	// SECURITY: Agent wall-clock skew intentionally fails closed here. Correct
	// the clock before retrying; do not relax the exact binding or proof checks.
	if deleted.GetId() == "" || deleted.GetId() != record.AssetID || deleted.GetOrganizationId() != identity.TenantUUID || deleted.GetDeviceId() != identity.EntityID || deleted.GetDeletedAt() == nil || deleted.GetDeletedAt().CheckValid() != nil || deleted.GetDeletedAt().AsTime().After(time.Unix(record.AuthorizedAt, 0).Add(time.Second)) {
		return fmt.Errorf("invalid exact Cloud deletion binding")
	}
	return nil
}
func (s *ProvisioningService) readProvisioningState() (*provisioningState, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		return nil, err
	}
	var state provisioningState
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// SECURITY: This validates only the Agent's root-owned 0600 provisioning state,
// written after operator mTLS and exact installed-leaf checks. Its signature
// does not establish Cloud authorization. Pending records never permit erasure.
// This is not a verifier of remote device identity: CLI receipt recovery anchors
// the historical leaf to configured roots before trusting public status.
func validateUnenrollmentState(state *provisioningState) (*unenrollproof.Completion, error) {
	if state.Unenrollment == nil {
		return nil, fmt.Errorf("reset authorization absent")
	}
	record, _, err := unenrollproof.ReadCompletion(state.Unenrollment.Receipt)
	if err != nil {
		return nil, err
	}
	if err := validateResetBinding(record); err != nil {
		return nil, err
	}
	switch state.Unenrollment.Status {
	case unenrollmentPending:
		if !state.Enrolled || state.PrincipalURI != record.Principal || state.CloudHost != record.Cloud || state.KeyPEM != "" {
			return nil, fmt.Errorf("reset authorization disagrees with enrollment state")
		}
		leaves, _ := certs.ParseCertsFromPEM([]byte(state.CertPEM))
		if len(leaves) == 0 || unenrollproof.Fingerprint(leaves[0]) != record.Fingerprint {
			return nil, fmt.Errorf("reset authorization certificate changed")
		}
	case unenrollmentCompleted:
		if state.Enrolled || state.CloudHost != "" || state.OrgID != 0 || state.AssetID != 0 || state.PrincipalURI != "" || state.KeyPEM != "" || state.CertPEM != "" || state.ChainPEM != "" || state.ACMEDirectoryURL != "" {
			return nil, fmt.Errorf("completed reset retains enrollment state")
		}
	default:
		return nil, fmt.Errorf("unknown reset recovery status")
	}
	return record, nil
}

func syncResetDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *ProvisioningService) recoverCloudReset() error {
	state, err := s.readProvisioningState()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.Unenrollment == nil {
		return nil
	}
	if _, err := validateUnenrollmentState(state); err != nil {
		return err
	}
	if state.Unenrollment.Status == unenrollmentPending {
		return fmt.Errorf("pending reset lacks Cloud-authenticated authorization; credentials retained, explicit local recovery required")
	}
	return syncResetDirectory(s.configPath)
}
func (s *ProvisioningService) cloudCompletion() ([]byte, error) {
	state, err := s.readProvisioningState()
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if state.Unenrollment == nil {
		return nil, nil
	}
	if _, err := validateUnenrollmentState(state); err != nil {
		return nil, err
	}
	if state.Unenrollment.Status != unenrollmentCompleted {
		return nil, fmt.Errorf("reset recovery is still pending")
	}
	return state.Unenrollment.Receipt, nil
}

// Cloud deletion protobufs supplied by callers are not Cloud-authenticated reset
// authorization. Operator mTLS and OCSP revocation alone cannot authorize erasure.
// Keep this RPC disabled until fresh-factor approval, a signed reset command,
// durable preparation before revocation, and authenticated Cloud evidence exist.
func (s *ProvisioningServiceV2) cloudUnprovision(context.Context, *agentpbv2.UnprovisionRequest) (*agentpbv2.UnprovisionResponse, error) {
	return nil, status.Error(codes.FailedPrecondition, "direct PKI reset is disabled pending Cloud-authenticated authorization and durable device preparation; keys retained")
}
