package commands

import (
	"fmt"
	"os"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// cloudGRPCForOrg returns the cloud gRPC endpoint of the auth session that owns
// a certificate for orgID, or "" if none is found. It maps the org carried by
// the verifying mTLS cert back to the cloud host that issued it.
func cloudGRPCForOrg(cfg *config.Config, orgID int) string {
	for _, auth := range cfg.Auth {
		for _, c := range auth.Certificates {
			if c.OrganizationID == orgID {
				return auth.CloudGRPC
			}
		}
	}
	return ""
}

func displayCloud(c string) string {
	if c == "" {
		return "an unknown cloud"
	}
	return c
}

// enforceDevicePin checks the (organisation, cloud host) pin for a freshly
// connected device (WDY-1149) and records or challenges it:
//
//   - first use → record the pin, proceed
//   - match     → proceed; a renewed or re-enrolled cert within the same
//     organisation + cloud is expected and never challenged
//   - mismatch  → warn in red and interactively ask whether to trust the new
//     identity; declining, or running non-interactively, aborts the connection
//
// Plaintext / unprovisioned connections carry no verifiable identity and are
// skipped. It is best-effort about local state: a config read/write failure
// never blocks an already-verified mTLS connection.
func enforceDevicePin(hostname string, conn *grpcclient.AgentConnection) error {
	if conn == nil || !conn.IsMTLS || conn.CertInfo == nil {
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	org := conn.CertInfo.OrganizationID
	cloud := cloudGRPCForOrg(cfg, org)

	switch cfg.EvaluateDevicePin(hostname, org, cloud) {
	case config.PinMatch:
		return nil
	case config.PinFirstUse:
		cfg.SetDevicePin(hostname, org, cloud)
		_ = config.Save(cfg)
		return nil
	default: // config.PinMismatch
		prev, _ := cfg.DevicePinFor(hostname)
		fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("Device %q now presents a different identity than the one you pinned.", hostname)))
		fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("  pinned: organization %d via %s", prev.OrgID, displayCloud(prev.CloudGRPC))))
		fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("  now:    organization %d via %s", org, displayCloud(cloud))))
		fmt.Fprintln(os.Stderr, tui.ErrorMessage("A renewed or re-enrolled certificate keeps the same organization and cloud, so this change is unexpected — it may be a man-in-the-middle or a swapped device."))

		if jsonOutput || !isInteractiveTerminal() {
			return fmt.Errorf("device %q identity changed (organization/cloud); refusing to connect — re-run 'wendy device set-default %s' to re-pin if this is expected", hostname, hostname)
		}
		trusted, cErr := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Trust the new identity for %q and re-pin it?", hostname))
		if cErr != nil || !trusted {
			return fmt.Errorf("device %q identity change was not trusted; connection aborted", hostname)
		}
		cfg.SetDevicePin(hostname, org, cloud)
		_ = config.Save(cfg)
		return nil
	}
}
