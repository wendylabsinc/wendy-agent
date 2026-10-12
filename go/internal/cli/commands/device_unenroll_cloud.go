package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/cloudunenroll"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

var resolveUnenrollTargetFn = resolveTarget

// Resolve cloud defaults before ordinary device resolution, which would require
// an online device and open a tunnel. Removing a cloud enrollment needs neither.
func cloudUnenrollTarget(cmd *cobra.Command, cloudOnly bool) (string, bool, error) {
	selector := deviceFlag
	_, cloudCommand := cloudDeviceConfigFromContext(cmd.Context())
	if cfg, ok := cloudDeviceConfigFromContext(cmd.Context()); ok && cfg.DeviceName != "" {
		selector = cfg.DeviceName
	}
	if selector == "" {
		cfg, err := config.Load()
		if err != nil {
			return "", false, err
		}
		// A local default is meaningful for --cloud-only when it is also the
		// enrolled name. The cloud group uses only cloud-scoped defaults.
		if !cloudCommand || strings.HasPrefix(cfg.DefaultDevice, "cloud:") {
			selector = cfg.DefaultDevice
		}
	}
	_, scoped, err := parseCloudDeviceSelector(selector)
	if err != nil {
		return "", false, err
	}
	return selector, cloudOnly || cloudCommand || scoped, nil
}

func resolveCloudUnenrollAuth(cloudGRPC, selector string) (*config.AuthConfig, string, error) {
	if scoped, matched, err := parseCloudDeviceSelector(selector); matched {
		if err != nil {
			return nil, "", err
		}
		if cloudGRPC != "" && cloudGRPC != scoped.Endpoint {
			return nil, "", fmt.Errorf("--cloud-grpc conflicts with the selected device's cloud endpoint")
		}
		cfg, err := config.Load()
		if err != nil {
			return nil, "", err
		}
		auth, err := scoped.auth(cfg)
		if scoped.AssetUUID != "" {
			return auth, scoped.AssetUUID, err
		}
		return auth, fmt.Sprint(scoped.AssetID), err
	}
	auth, err := resolveAuthEntry(cloudGRPC)
	return auth, selector, err
}

var resolveCloudUnenrollAuthFn = resolveCloudUnenrollAuth

// Match a local Lite board only by its hardware-derived PKI identity. An
// enrolled name may differ from the name still stored on the board.
func resolveUnenrollDevice(devices []cloudDiscoveryDevice, selector, deviceID string) (cloudDiscoveryDevice, error) {
	identity := deviceID
	if identity == "" {
		if selector == "" {
			return resolveCloudDiscoveryDevice(devices, selector)
		}
		for _, device := range devices {
			if strings.EqualFold(device.key, selector) {
				return device, nil
			}
		}
		identity = selector
	}
	var matches []cloudDiscoveryDevice
	for _, device := range devices {
		identityMatch := device.v2 != nil && device.v2.GetPkiDeviceName() == identity
		nameMatch := deviceID == "" && strings.EqualFold(device.GetName(), selector)
		if identityMatch || nameMatch {
			matches = append(matches, device)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return cloudDiscoveryDevice{}, fmt.Errorf("no cloud enrollment matches %q in the selected organization; use 'wendy cloud discover --all --json' to find its asset ID", identity)
	default:
		return cloudDiscoveryDevice{}, fmt.Errorf("multiple cloud enrollments match %q; select the exact asset ID with --device", identity)
	}
}

func runCloudDeviceUnenroll(cmd *cobra.Command, cloudGRPC, selector, deviceID string, assumeYes bool) error {
	if cfg, ok := cloudDeviceConfigFromContext(cmd.Context()); ok && cloudGRPC == "" {
		cloudGRPC = cfg.CloudGRPC
	}
	auth, selector, err := resolveCloudUnenrollAuthFn(cloudGRPC, strings.TrimSpace(selector))
	if err != nil {
		return err
	}
	auth, err = prepareEnrollmentAuth(cmd.Context(), auth)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
	defer cancel()
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return err
	}
	defer conn.Close()
	cloudCtx, err := cloudContext(ctx, auth)
	if err != nil {
		return err
	}
	var device cloudDiscoveryDevice
	// Exact UUID selection also works for incomplete enrollments that a
	// compute-device listing might exclude. Check the tenant before mutation.
	if _, parseErr := uuid.Parse(selector); parseErr == nil && len(auth.Certificates) > 0 && auth.Certificates[0].TenantUUID() != "" {
		asset, err := cloudpbv2.NewAssetServiceClient(conn).GetAsset(cloudCtx, &cloudpbv2.GetAssetRequest{Id: selector})
		if err != nil {
			return fmt.Errorf("reading cloud asset %s: %w", selector, err)
		}
		device = cloudDiscoveryDevice{cloudAssetMetadata: asset, v2: asset, key: asset.GetId()}
	} else {
		devices, err := fetchCloudDiscoveryDevices(ctx, auth, false)
		if err != nil {
			return err
		}
		device, err = resolveUnenrollDevice(devices, selector, deviceID)
		if err != nil {
			return err
		}
	}
	if device.v2 != nil {
		if device.v2.GetOrganizationId() != auth.Certificates[0].TenantUUID() {
			return fmt.Errorf("cloud asset does not belong to the selected organization")
		}
		if device.v2.GetPkiDeviceName() == "" {
			return fmt.Errorf("asset %s has no device enrollment binding", device.key)
		}
	}
	if !assumeYes {
		if !isInteractiveTerminal() {
			return fmt.Errorf("unenroll is destructive; pass --yes to confirm removing %q (asset %s) from %s", device.GetName(), device.key, auth.CloudGRPC)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "This will revoke the cloud enrollment for %q (asset %s) in %s and delete its asset record. The device will not be contacted or reset.\n", device.GetName(), device.key, auth.CloudGRPC)
		if !confirmDefaultNoFn("Continue?") {
			fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
			return nil
		}
	}
	var result cloudunenroll.Result
	if device.v2 != nil {
		result, err = cloudunenroll.Remove(cloudCtx, conn, auth, device.v2)
	} else {
		result.CertificatesRevoked, result.AssetDeleted, err = cloudUnenrollCleanupClient(cloudCtx, conn, device.legacy.GetId())
	}
	if jsonOutput {
		out := map[string]any{
			"assetId": device.key, "cloud": auth.CloudGRPC, "deviceReset": false,
			"certsRevoked": result.CertificatesRevoked, "credentialsRevoked": result.CredentialsRevoked,
			"assetDeleted": result.AssetDeleted,
		}
		if err != nil {
			out["cloudError"] = err.Error()
		}
		if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(out); encodeErr != nil {
			return encodeErr
		}
	} else if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Unenrolled %s: revoked %d certificate(s) and %d enrollment credential(s), and deleted cloud asset %s.\n", device.GetName(), result.CertificatesRevoked, result.CredentialsRevoked, device.key)
		fmt.Fprintln(cmd.OutOrStdout(), "The device was not reset. If it still holds an issued identity, clear it through device recovery before enrolling again.")
	}
	if err != nil {
		return fmt.Errorf("cloud unenrollment of asset %s failed after revoking %d certificate(s) and %d enrollment credential(s): %w", device.key, result.CertificatesRevoked, result.CredentialsRevoked, err)
	}
	return nil
}
