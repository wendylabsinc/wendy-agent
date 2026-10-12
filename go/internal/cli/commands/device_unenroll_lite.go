package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// Current Lite firmware exposes its identity but has no command to erase its
// enrollment credentials. Unenrollment removes the matching cloud asset without
// replacing the board's configuration or resetting it.
type liteUnenrollmentDevice interface {
	GetDeviceIdentity(time.Duration) (*liteclient.DeviceIdentity, error)
	Close() error
}

var (
	connectLiteUnenrollmentFn = func(target models.ExternalDevice) (liteUnenrollmentDevice, error) {
		return (&providers.MicroWendyProvider{}).ConnectEnrollment(target)
	}
	unenrollLiteCloudFn = runCloudDeviceUnenroll
)

func runLiteDeviceUnenroll(cmd *cobra.Command, target models.ExternalDevice, cloudGRPC string, assumeYes bool) error {
	device, err := connectLiteUnenrollmentFn(target)
	if err != nil {
		return fmt.Errorf("connecting to Wendy Lite device: %w; if its firmware is unavailable, use 'wendy device unenroll --cloud-only --device <asset-id-or-name>'", err)
	}
	identity, err := device.GetDeviceIdentity(5 * time.Second)
	device.Close()
	if err != nil {
		return fmt.Errorf("reading Wendy Lite device identity: %w; remove its cloud enrollment with 'wendy device unenroll --cloud-only --device <asset-id-or-name>'", err)
	}
	if identity == nil || strings.TrimSpace(identity.ID) == "" {
		return fmt.Errorf("Wendy Lite device returned no device ID; remove its cloud enrollment with 'wendy device unenroll --cloud-only --device <asset-id-or-name>'")
	}

	// Derive the same PKI device ID as enrollment. Discovery names and serial
	// paths are not cloud identifiers, and a failed enrollment may not yet have
	// issued a certificate for this device.
	return unenrollLiteCloudFn(cmd, cloudGRPC, "", "lite-"+identity.ID, assumeYes)
}
