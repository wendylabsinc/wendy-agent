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

// observedDeviceIdentity is what a live connection actually proved about the
// device answering at a hostname. All of it comes from a certificate the CLI
// verified, or — when mTLS is false — from the absence of any certificate at
// all, which is itself the signal enforceDeviceIdentity acts on.
type observedDeviceIdentity struct {
	// mTLS is true when the connection was authenticated. False means the
	// device answered unauthenticated: it is not provisioned.
	mTLS bool
	// orgID is the organisation of the CLI certificate that authenticated.
	orgID int
	// assetID is the device's cloud asset id from the verified server cert's
	// "urn:wendy:org:<org>:asset:<assetID>" SAN. Empty when the agent's
	// certificate carries no asset identity (legacy certs).
	assetID string
}

// observeDeviceIdentity reads what conn proved about the device it reached.
func observeDeviceIdentity(conn *grpcclient.AgentConnection) observedDeviceIdentity {
	if conn == nil || !conn.IsMTLS || conn.CertInfo == nil {
		return observedDeviceIdentity{}
	}
	obs := observedDeviceIdentity{mTLS: true, orgID: conn.CertInfo.OrganizationID}
	// Only an "asset" entity is a device; a "user" URN on a server cert would
	// be a misissued certificate, and pinning it would be meaningless.
	if id, ok := conn.ObservedServerIdentity(); ok && id.EntityType == "asset" {
		obs.assetID = id.EntityID
	}
	return obs
}

// enforceDevicePin checks the (organisation, cloud host, asset) pin for a
// freshly connected device (WDY-1149) and records or challenges it.
func enforceDevicePin(hostname string, conn *grpcclient.AgentConnection) error {
	if conn == nil {
		return nil
	}
	return enforceDeviceIdentity(hostname, observeDeviceIdentity(conn))
}

// enforceDeviceIdentity compares what a connection proved about a device
// against the pin recorded for its hostname:
//
//   - first use   → record the pin, proceed
//   - match       → proceed; a renewed or re-enrolled cert for the same
//     organisation + cloud + asset is expected and never challenged
//   - legacy pin  → backfill the observed asset id, proceed
//   - mismatch    → warn in red and interactively ask whether to trust the new
//     identity; declining, or running non-interactively, aborts the connection
//   - unprovisioned, but pinned → same challenge: a device we have seen
//     enrolled answering with no identity at all has been reflashed, factory
//     reset, or replaced by something squatting its name
//
// A device with no pin that answers unprovisioned is the ordinary
// out-of-the-box case and passes silently. It is best-effort about local state:
// a config read/write failure never blocks an already-verified connection.
func enforceDeviceIdentity(hostname string, obs observedDeviceIdentity) error {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}

	if !obs.mTLS {
		return challengeUnprovisionedDevice(cfg, hostname)
	}

	cloud := cloudGRPCForOrg(cfg, obs.orgID)
	switch cfg.EvaluateDevicePin(hostname, obs.orgID, cloud, obs.assetID) {
	case config.PinMatch:
		return nil
	case config.PinFirstUse, config.PinAdoptAsset:
		// PinAdoptAsset is a pin written before asset ids were recorded: org and
		// cloud already match, so this is a silent upgrade, not a challenge.
		cfg.SetDevicePin(hostname, obs.orgID, cloud, obs.assetID)
		_ = config.Save(cfg)
		return nil
	default: // config.PinMismatch
		prev, _ := cfg.DevicePinFor(hostname)
		printIdentityChangeWarning(hostname, prev, obs, cloud)

		if jsonOutput || !isInteractiveTerminal() {
			return fmt.Errorf("device %q identity changed (organization/cloud/asset); refusing to connect — re-run 'wendy device set-default %s' to re-pin if this is expected", hostname, hostname)
		}
		trusted, cErr := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Trust the new identity for %q and re-pin it?", hostname))
		if cErr != nil || !trusted {
			return fmt.Errorf("device %q identity change was not trusted; connection aborted", hostname)
		}
		cfg.SetDevicePin(hostname, obs.orgID, cloud, obs.assetID)
		_ = config.Save(cfg)
		return nil
	}
}

// printIdentityChangeWarning explains a pin mismatch in terms of what actually
// changed. A different asset within the same org+cloud is a different physical
// device; a different org or cloud is a different trust domain entirely.
func printIdentityChangeWarning(hostname string, prev config.DevicePin, obs observedDeviceIdentity, cloud string) {
	fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("Device %q now presents a different identity than the one you pinned.", hostname)))
	fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("  pinned: organization %d via %s%s", prev.OrgID, displayCloud(prev.CloudGRPC), assetSuffix(prev.AssetID))))
	fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("  now:    organization %d via %s%s", obs.orgID, displayCloud(cloud), assetSuffix(obs.assetID))))
	if prev.OrgID == obs.orgID && prev.CloudGRPC == cloud {
		fmt.Fprintln(os.Stderr, tui.ErrorMessage("Same organization and cloud, but a different device: this hostname now resolves to another machine, or the device was wiped and re-enrolled as a new asset."))
	} else {
		fmt.Fprintln(os.Stderr, tui.ErrorMessage("A renewed or re-enrolled certificate keeps the same organization and cloud, so this change is unexpected — it may be a man-in-the-middle or a swapped device."))
	}
}

// challengeUnprovisionedDevice handles a connection with no verifiable identity
// at all. Only a hostname we have previously seen enrolled is challenged: for
// everything else, connecting to an unprovisioned device is the normal
// out-of-the-box flow.
//
// This case cannot be folded into EvaluateDevicePin — there is no observed
// identity to compare — but it is the same trust question: the device that
// vouched for this hostname is not the one answering now.
func challengeUnprovisionedDevice(cfg *config.Config, hostname string) error {
	prev, pinned := cfg.DevicePinFor(hostname)
	if !pinned {
		return nil
	}

	fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("Device %q was enrolled, but is now answering without an identity.", hostname)))
	fmt.Fprintln(os.Stderr, tui.ErrorMessage(fmt.Sprintf("  pinned: organization %d via %s%s", prev.OrgID, displayCloud(prev.CloudGRPC), assetSuffix(prev.AssetID))))
	fmt.Fprintln(os.Stderr, tui.ErrorMessage("  now:    unprovisioned (no mTLS)"))
	fmt.Fprintln(os.Stderr, tui.ErrorMessage("An enrolled device does not drop its certificate on its own — it has been reflashed or factory reset, another machine has taken its name or address, or this CLI no longer holds credentials for its organization (try 'wendy auth login'). Anything you run over this connection would be unauthenticated."))

	if jsonOutput || !isInteractiveTerminal() {
		return fmt.Errorf("device %q was enrolled but is now answering unprovisioned; refusing to connect — re-enroll it, check 'wendy auth login', or run 'wendy device set-default %s' to accept the change", hostname, hostname)
	}
	trusted, cErr := tui.ConfirmNoDefaultDanger(fmt.Sprintf("Continue to %q unauthenticated and forget its pinned identity?", hostname))
	if cErr != nil || !trusted {
		return fmt.Errorf("device %q unexpected loss of identity was not trusted; connection aborted", hostname)
	}
	// Accepted: there is no identity left to pin, so drop the stale one rather
	// than re-challenging on every subsequent command.
	cfg.ClearDevicePin(hostname)
	_ = config.Save(cfg)
	return nil
}

// clearDevicePinForRepin drops the stored pin for hostname so the next
// successful connection records a fresh one. `wendy device set-default <host>`
// calls it because naming a device on the command line is the user asserting
// they mean that device — and it is the escape hatch both refusal paths above
// point at, which would otherwise be a dead end: set-default's own connect
// would hit the same refusal and never reach the re-pin. Best-effort; a config
// read/write failure just leaves the old pin in place.
func clearDevicePinForRepin(hostname string) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	if _, pinned := cfg.DevicePinFor(hostname); !pinned {
		return
	}
	cfg.ClearDevicePin(hostname)
	_ = config.Save(cfg)
}

func assetSuffix(assetID string) string {
	if assetID == "" {
		return ""
	}
	return fmt.Sprintf(", asset %s", assetID)
}
