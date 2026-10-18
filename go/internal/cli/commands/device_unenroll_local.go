package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	grpcpeer "google.golang.org/grpc/peer"
)

// Called only after an authenticated Cloud lookup reports an absent asset.
// Local cleanup has its own capability and acknowledgement; it cannot produce
// or stand in for Cloud deletion or certificate revocation evidence.
func runV2LocalDeviceUnenroll(ctx context.Context, conn *grpcclient.AgentConnection, prov *agentpbv2.ProvisionedResponse, verifiedPeer *grpcpeer.Peer, yes, checkOnly bool) error {
	if !prov.GetLocalResetSupported() {
		return fmt.Errorf("Cloud asset not found; this Agent does not support local enrollment reset; update it with 'wendy device update' and retry; no mutation performed")
	}
	principal, err := certs.ParsePrincipal(prov.GetPrincipalUri())
	if err != nil || principal.EntityType != certs.EntityAsset {
		return fmt.Errorf("invalid direct PKI device identity")
	}
	fingerprint, err := directUnenrollPeerFingerprint(conn, verifiedPeer, principal)
	if err != nil {
		return err
	}
	if conn.CertInfo == nil {
		return fmt.Errorf("local enrollment reset requires the connected operator certificate")
	}
	operator, err := certs.ParsePrincipal(conn.CertInfo.CertificatePrincipal())
	if err != nil || operator.EntityType != certs.EntityUser || operator.TenantUUID != principal.TenantUUID {
		return fmt.Errorf("local enrollment reset requires a same-tenant operator certificate")
	}
	if checkOnly {
		if jsonOutput {
			out, _ := json.Marshal(map[string]any{"principal": principal.Principal, "cloudAssetAbsent": true, "localResetSupported": true, "revocationAttempted": false, "assetDeleted": false, "deviceReset": false})
			fmt.Println(string(out))
		} else {
			fmt.Println("Cloud asset not found. Authenticated local enrollment reset is supported. No changes made.")
		}
		return nil
	}
	if !yes {
		if jsonOutput || !isInteractiveTerminalFn() {
			return fmt.Errorf("Cloud asset not found; pass --yes to confirm clearing this device's local enrollment without Cloud deletion or certificate revocation")
		}
		fmt.Printf("Cloud asset not found. Clear the local enrollment for %s? This does not revoke its certificate or delete a Cloud record.\n", principal.Principal)
		if !confirmDefaultNoFn("Continue?") {
			return nil
		}
	}
	response, resetErr := agentpbv2.NewWendyProvisioningServiceClient(conn.Conn).Unprovision(ctx, &agentpbv2.UnprovisionRequest{
		ExpectedPrincipalUri: principal.Principal, ExpectedCertificateSha256: fingerprint, LocalOnly: true,
	})
	completed := resetErr == nil && response.GetLocalResetCompleted()
	if jsonOutput {
		out, _ := json.MarshalIndent(struct {
			v2UnenrollProgress
			LocalOnly        bool `json:"localOnly"`
			CloudAssetAbsent bool `json:"cloudAssetAbsent"`
		}{v2UnenrollProgress: v2UnenrollProgress{Cloud: prov.GetCloudHost(), Principal: principal.Principal, Fingerprint: fingerprint, Reset: completed}, LocalOnly: true, CloudAssetAbsent: true}, "", "  ")
		fmt.Println(string(out))
	}
	if resetErr != nil {
		return fmt.Errorf("Cloud asset not found; local enrollment reset unconfirmed: %w", resetErr)
	}
	if !completed {
		return fmt.Errorf("Agent did not confirm local enrollment reset; local identity pins retained")
	}
	if err := config.Update(func(cfg *config.Config) (bool, error) { clearPinsForIdentity(cfg, principal); return true, nil }); err != nil {
		return fmt.Errorf("local enrollment cleared; local identity pin cleanup failed: %w", err)
	}
	if !jsonOutput {
		fmt.Println("Cloud asset not found. Local enrollment cleared; the device can enroll again. Certificate revocation was not performed.")
	}
	return nil
}
