package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
)

// newDeviceUnpinCmd is the escape hatch every identity refusal in
// enforceDeviceIdentity and challengeUnprovisionedDevice points at by name.
// Until this command exists, hitting one of those refusals is a dead end: the
// CLI tells the user to run it, and there is nothing to run.
//
// Unpinning clears local trust state only. It never dials the device — the
// whole point is to work when the device is offline, wiped, or gone, which is
// exactly when a pin most needs clearing.
func newDeviceUnpinCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unpin <hostname>",
		Short: "Clear the recorded identity pin for a device",
		Long: "Clear the recorded identity pin for a device, so the next connection to it\n" +
			"records a fresh identity instead of being challenged against the old one.\n" +
			"Use this after a legitimate reflash, factory reset, or re-enrollment —\n" +
			"anything that made 'wendy device unpin' the CLI's own suggestion.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname := args[0]
			// pinKeyForAddr, not the raw argument: a pin recorded via
			// `--device host.local:50051` files under "host.local" (the port
			// stripped), and a user unpinning must be able to hand back exactly
			// what they used to connect. This is the same bug fixed in
			// set-default in Task 6 — do not reintroduce it here.
			pinKey := pinKeyForAddr(hostname)

			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("loading config: %w", err)
			}

			clearPinsGoverning(cfg, pinKey)

			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Unpinned %q. The next connection to it will record a fresh identity.\n", hostname)
			return nil
		},
	}
}

// clearPinsGoverning drops every pin that could govern a dial to pinKey — the
// config pins under each of the device's candidate keys, plus the SPKI entries
// those pins identify — and reports whether any config pin was actually
// removed. It mutates cfg; saving is the caller's job.
//
// It clears EVERY candidate rather than only the one lookupPin would return,
// because "unpin" has to terminate. A device pinned under both its mDNS
// hostname and the cloud roster's asset name would otherwise refuse under one
// pin, be unpinned, and refuse again under the other — a user watching that
// happen learns the command does not work. Consulting the same candidate list
// the dial path uses is what keeps the two in step: whatever a refusal could be
// caused by is what this clears.
//
// SPKI entries are cleared unconditionally, not only when a config pin was
// found. The two stores are written on different paths — getAgentVersionAtAddress
// runs the ladder with the SPKI store for every device the mDNS prober
// enumerates, so `wendy device list` alone leaves SPKI entries with no config
// pin behind them — and an escape hatch that only reaches one of them leaves
// the other's refusal permanent.
func clearPinsGoverning(cfg *config.Config, pinKey string) bool {
	if cfg == nil || pinKey == "" {
		return false
	}
	cleared := false
	var identityKeys []string
	for _, key := range pinCandidateKeys(pinKey) {
		pin, ok := cfg.DevicePinFor(key)
		if !ok {
			continue
		}
		cfg.ClearDevicePin(key)
		cleared = true
		// Read the pin BEFORE it is gone: it is the only place the (org, asset)
		// pair needed to compute the SPKI store's key lives. A legacy pin (no
		// AssetID) has no derivable key — not a failure, just nothing to remove.
		if pin.AssetID != "" {
			identityKeys = append(identityKeys, certs.WendyIdentity{
				OrgID:      int32(pin.OrgID),
				EntityType: "asset",
				EntityID:   pin.AssetID,
			}.IdentityKey())
		}
	}
	if key := cachedIdentityKey(pinKey); key != "" {
		identityKeys = append(identityKeys, key)
	}
	removeSPKIPins(identityKeys)
	return cleared
}

// cachedIdentityKey derives the SPKI store's key for pinKey from the discovery
// cache, which records the asset and org a device advertised. It is the only
// lookup that reaches an SPKI entry belonging to a host with no asset-bearing
// config pin — the `wendy device list` case above, where nothing else in local
// state ties the hostname the user types to the asset URN the store is keyed
// by.
//
// Reading unauthenticated discovery data here is safe in the way it is not on
// the dial path: the worst an attacker-chosen asset id can do is make an unpin
// remove a pin the user was already asking to remove. Returns "" when the cache
// is unavailable, has no entry, or the entry carries no asset identity.
func cachedIdentityKey(pinKey string) string {
	entry, ok := cachedDeviceHostEntry(pinKey)
	if !ok || entry.AssetID <= 0 || entry.OrgID <= 0 {
		return ""
	}
	return certs.WendyIdentity{
		OrgID:      entry.OrgID,
		EntityType: "asset",
		EntityID:   strconv.Itoa(int(entry.AssetID)),
	}.IdentityKey()
}

// removeSPKIPins drops the given identity keys from the SPKI pin store.
// Best-effort: an unopenable store leaves the config side of the unpin done,
// which is strictly better than failing the command outright.
func removeSPKIPins(identityKeys []string) {
	if len(identityKeys) == 0 {
		return
	}
	// Same config-directory resolution as openPinStore, but that helper returns
	// certs.PinChecker, which only exposes CheckAndUpdate — not Remove. Open the
	// concrete store directly.
	dir, err := config.ConfigDir()
	if err != nil {
		return
	}
	store, err := devicepin.Open(dir)
	if err != nil {
		return
	}
	for _, key := range identityKeys {
		_ = store.Remove(key)
	}
}
