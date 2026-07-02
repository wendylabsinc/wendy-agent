//go:build darwin || linux

package t234

import (
	"context"
	"fmt"
	"time"
)

// The USB gadget the flashing initrd exposes (Linux Foundation composite
// gadget IDs, from meta-tegra's initrd-flash.scheme.in).
const (
	GadgetVendorID  = 0x1d6b
	GadgetProductID = 0x0104
)

// SCSI inquiry vendor strings of the exported LUNs (init-flash.sh writes
// "<export_name><serial>" into the gadget's inquiry_string; the vendor field
// is the first 8 characters, i.e. the export name).
const FlashpkgVendor = "flashpkg"

// UMSDisk is one USB mass-storage LUN the flashing initrd exposed.
type UMSDisk struct {
	DevPath   string // e.g. /dev/disk4 or /dev/sdb
	RawPath   string // e.g. /dev/rdisk4 (same as DevPath on Linux)
	SizeBytes int64
	Vendor    string // SCSI inquiry vendor, e.g. "flashpkg" or "mmcblk0"
	Serial    string // SCSI inquiry product: the device's 8-hex session id
}

// WaitForUMSDisk polls until a LUN with the given SCSI vendor string appears
// (the flashing initrd names LUNs after what they carry: "flashpkg" or the
// rootfs device). It returns an error when several match — wendy flashes one
// Orin at a time and must not write into the wrong board.
func WaitForUMSDisk(ctx context.Context, vendor string, timeout time.Duration) (UMSDisk, error) {
	deadline := time.Now().Add(timeout)
	for {
		disks, err := listUMSDisks()
		if err == nil {
			var matches []UMSDisk
			for _, d := range disks {
				if d.Vendor == vendor {
					matches = append(matches, d)
				}
			}
			switch {
			case len(matches) == 1:
				return matches[0], nil
			case len(matches) > 1:
				return UMSDisk{}, fmt.Errorf("found %d USB storage devices named %q — connect only one Jetson while flashing", len(matches), vendor)
			}
			// The device exports "flashpkg" instead of the requested LUN when
			// its side of the flash failed early — surface that instead of
			// timing out (mirrors the bundle's initrd-flash host script).
			if vendor != FlashpkgVendor {
				for _, d := range disks {
					if d.Vendor == FlashpkgVendor {
						return UMSDisk{}, fmt.Errorf("device exported %q instead of %q — the device-side flash failed early; its logs are in the flash package", FlashpkgVendor, vendor)
					}
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return UMSDisk{}, fmt.Errorf("timed out waiting for USB storage %q (last scan error: %v)", vendor, err)
			}
			return UMSDisk{}, fmt.Errorf("timed out waiting for USB storage %q from the device", vendor)
		}
		select {
		case <-ctx.Done():
			return UMSDisk{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
