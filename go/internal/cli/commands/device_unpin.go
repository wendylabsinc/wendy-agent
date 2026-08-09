package commands

import (
	"fmt"

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

			// Read the config pin BEFORE clearing it: it is the only place the
			// (org, asset) pair needed to compute the SPKI store's key still
			// lives. Clear first and that information is gone.
			pin, hadPin := cfg.DevicePinFor(pinKey)
			cfg.ClearDevicePin(pinKey)

			// A legacy pin (no AssetID) has no derivable SPKI key. That is not a
			// failure: clear the config pin and succeed, same as any other
			// unpin.
			if hadPin && pin.AssetID != "" {
				// Same config-directory resolution as openPinStore, but that
				// helper returns certs.PinChecker, which only exposes
				// CheckAndUpdate — not Remove. Open the concrete store directly.
				if dir, dirErr := config.ConfigDir(); dirErr == nil {
					if store, openErr := devicepin.Open(dir); openErr == nil {
						key := certs.WendyIdentity{
							OrgID:      int32(pin.OrgID),
							EntityType: "asset",
							EntityID:   pin.AssetID,
						}.IdentityKey()
						_ = store.Remove(key)
					}
				}
			}

			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Unpinned %q. The next connection to it will record a fresh identity.\n", hostname)
			return nil
		},
	}
}
