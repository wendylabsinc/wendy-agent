package commands

import (
	"fmt"
	"os"
	"strconv"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// seedPinsFromCloudAssets records the org's asset roster as cloud-sourced pins.
// The cloud spoke over an authenticated session, so this is authority, not a
// sighting: it overwrites whatever a LAN observation recorded, and closes the
// trust-on-first-use window for every device the cloud knows about before the
// CLI ever meets it on the network.
//
// An asset with no name has no key to pin under and is skipped rather than
// filed under "". config.Save is only called when at least one pin actually
// changed, so a roster of unnamed assets (or an empty roster) never touches
// disk.
func seedPinsFromCloudAssets(assets []*cloudpb.Asset, orgID int, cloudGRPC string) error {
	cfg, err := loadConfigForPinFn()
	if err != nil {
		return err
	}
	changed := false
	for _, a := range assets {
		name := a.GetName()
		if name == "" {
			continue
		}
		cfg.SetDevicePinFrom(name, orgID, cloudGRPC, strconv.Itoa(int(a.GetId())), config.PinSourceCloud)
		changed = true
	}
	if !changed {
		return nil
	}
	return config.Save(cfg)
}

// seedPinsFromAssetsBestEffort seeds pins from a verified cloud asset roster,
// deriving the org and cloud host from the auth session that fetched it.
//
// Best-effort by design: this runs after a caller's own fetchCloudAssetsFiltered
// call has already succeeded, so a seeding failure (e.g. an unwritable config
// file) must never fail the command the user actually ran — 'wendy cloud
// discover' must still list devices even if pins cannot be written. Errors are
// therefore swallowed and only surfaced via WENDY_TLS_DEBUG.
func seedPinsFromAssetsBestEffort(auth *config.AuthConfig, assets []*cloudpb.Asset) {
	if auth == nil || len(auth.Certificates) == 0 {
		return
	}
	orgID := auth.Certificates[0].OrganizationID
	if err := seedPinsFromCloudAssets(assets, orgID, auth.CloudGRPC); err != nil {
		debugPinSeed("seeding cloud pins for org %d failed: %v", orgID, err)
	}
}

// debugPinSeed logs only when WENDY_TLS_DEBUG is set, matching the CLI's other
// mTLS/pin diagnostics (see debugClock in device_clock.go).
func debugPinSeed(format string, args ...any) {
	if os.Getenv("WENDY_TLS_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[pin-seed] "+format+"\n", args...)
	}
}
