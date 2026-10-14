package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Output only: durable recovery evidence belongs to Cloud and Agent, not CLI.
type v2UnenrollProgress struct {
	Cloud       string `json:"cloud"`
	Principal   string `json:"principal"`
	AssetID     string `json:"assetId"`
	Fingerprint string `json:"certificateSHA256,omitempty"`
	Serial      string `json:"certificateSerial,omitempty"`
	Revoked     bool   `json:"certificateRevoked"`
	Deleted     bool   `json:"assetDeleted"`
	Reset       bool   `json:"deviceReset"`
}

func isLegacyCloudEnrollment(orgID, assetID int32) bool { return orgID > 0 && assetID > 0 }

type v2AssetState struct {
	Active  *cloudpbv2.Asset
	Deleted *cloudpbv2.DeletedAsset
}

// A status code or message alone is never deletion evidence.
func v2AssetLookupResult(asset *cloudpbv2.Asset, err error) (*v2AssetState, error) {
	if err == nil {
		return &v2AssetState{Active: asset}, nil
	}
	if status.Code(err) != codes.NotFound {
		return nil, err
	}
	var deleted *cloudpbv2.DeletedAsset
	for _, detail := range status.Convert(err).Details() {
		if d, ok := detail.(*cloudpbv2.DeletedAsset); ok {
			if deleted != nil {
				return nil, fmt.Errorf("ambiguous Cloud deletion details")
			}
			deleted = d
		}
	}
	if deleted == nil {
		return nil, fmt.Errorf("Cloud asset unknown; no retained deletion proof: %w", err)
	}
	return &v2AssetState{Deleted: deleted}, nil
}

type v2UnenrollOps struct {
	lookup func(context.Context, string) (*v2AssetState, error)
	delete func(context.Context, string) error
	proof  func(context.Context) ([]byte, error)
	reset  func(context.Context, string, string, []byte, *cloudpbv2.DeletedAsset) error
}

func checkV2UnenrollBinding(asset *cloudpbv2.Asset, j v2UnenrollProgress) error {
	id, err := certs.ParsePrincipal(j.Principal)
	if err != nil || id.EntityType != certs.EntityAsset || asset == nil || asset.GetId() != j.AssetID || asset.GetOrganizationId() != id.TenantUUID || asset.GetPkiDeviceName() != id.EntityID {
		return fmt.Errorf("refusing cleanup: exact Cloud asset/tenant/PKI binding does not match the authenticated device")
	}
	return nil
}

func v2UnenrollLifecycle(reply *v2AssetState, j *v2UnenrollProgress) (bool, error) {
	if reply == nil || (reply.Active != nil && reply.Deleted != nil) {
		return false, fmt.Errorf("invalid Cloud lifecycle evidence")
	}
	var asset *cloudpbv2.Asset
	deleted := false
	if reply.Deleted != nil {
		d := reply.Deleted
		if d == nil || d.GetDeletedAt() == nil || d.GetDeletedAt().CheckValid() != nil {
			return false, fmt.Errorf("invalid Cloud deletion evidence; keys retained")
		}
		asset = &cloudpbv2.Asset{Id: d.GetId(), OrganizationId: d.GetOrganizationId(), PkiDeviceName: &d.DeviceId}
		deleted = true
	} else {
		asset = reply.Active
	}
	if asset == nil {
		return false, fmt.Errorf("missing Cloud lifecycle identity")
	}
	parsed, err := uuid.Parse(asset.GetId())
	if err != nil || parsed.String() != asset.GetId() {
		return false, fmt.Errorf("invalid Cloud asset UUID")
	}
	candidate := *j
	if candidate.AssetID == "" {
		candidate.AssetID = asset.GetId()
	}
	if err := checkV2UnenrollBinding(asset, candidate); err != nil {
		return false, err
	}
	j.AssetID = candidate.AssetID
	return deleted, nil
}

func performV2Unenroll(ctx context.Context, j *v2UnenrollProgress, ops v2UnenrollOps) error {
	if len(j.Fingerprint) != 64 {
		return fmt.Errorf("verified installed certificate fingerprint required")
	}
	if _, err := hex.DecodeString(j.Fingerprint); err != nil {
		return fmt.Errorf("invalid installed certificate fingerprint")
	}
	reply, err := ops.lookup(ctx, j.AssetID)
	if err != nil {
		return fmt.Errorf("preflight Cloud asset lifecycle: %w", err)
	}
	// Cloud owns the only revocation/deletion workflow. Every retry reads its
	// durable binding, never a CLI journal or a previous transient response.
	deleted, err := v2UnenrollLifecycle(reply, j)
	if err != nil {
		return err
	}
	if !deleted {
		if err := ops.delete(ctx, j.AssetID); err != nil {
			return fmt.Errorf("Cloud unenrollment failed/uncertain; keys retained: %w", err)
		}
	}
	// A successful delete response alone is not retained binding evidence.
	reply, err = ops.lookup(ctx, j.AssetID)
	if err != nil {
		return fmt.Errorf("confirming Cloud tombstone; keys retained: %w", err)
	}
	deleted, err = v2UnenrollLifecycle(reply, j)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("Cloud asset still active; reset refused")
	}
	j.Deleted = true
	proof, err := ops.proof(ctx)
	if err != nil {
		return fmt.Errorf("verifying installed-leaf PKI revocation; keys retained: %w", err)
	}
	j.Revoked = true
	if err := ops.reset(ctx, j.Principal, j.Fingerprint, proof, reply.Deleted); err != nil {
		return fmt.Errorf("certificate revoked and asset deleted; local reset unconfirmed: %w", err)
	}
	j.Reset = true
	return nil
}

func directUnenrollPeerFingerprint(conn *grpcclient.AgentConnection, verifiedPeer *grpcpeer.Peer, principal certs.WendyIdentity) (string, error) {
	if conn == nil || !conn.IsMTLS || conn.IsSessionProxy || verifiedPeer == nil {
		return "", fmt.Errorf("v2 unenroll requires a directly verified mTLS device connection; use its LAN hostname/IP")
	}
	tlsInfo, ok := verifiedPeer.AuthInfo.(credentials.TLSInfo)
	if !ok || !tlsInfo.State.HandshakeComplete || len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("verified peer certificate unavailable; no mutation performed")
	}
	leaf := tlsInfo.State.PeerCertificates[0]
	peerPrincipal, ok := certs.TenantPrincipalFromCert(leaf)
	if !ok || peerPrincipal != principal.Principal {
		return "", fmt.Errorf("Agent state disagrees with its verified TLS identity")
	}
	digest := sha256.Sum256(leaf.Raw)
	return hex.EncodeToString(digest[:]), nil
}

// A previous development Agent advertised reset support without the required
// authorization protocol. Do not trust that capability to start irreversible
// Cloud revocation/deletion, including when --yes or --check is supplied.
func runV2DeviceUnenroll(context.Context, *grpcclient.AgentConnection, *agentpbv2.ProvisionedResponse, *grpcpeer.Peer, string, string, bool, bool) error {
	return fmt.Errorf("direct PKI unenrollment is disabled pending Cloud-authenticated reset authorization and durable device preparation; no mutation performed")
}
