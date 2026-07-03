//go:build darwin || linux

package t234

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// The USB gadget the flashing initrd exposes (Linux Foundation composite
// gadget IDs, from meta-tegra's initrd-flash.scheme.in).
const (
	GadgetVendorID  = 0x1d6b
	GadgetProductID = 0x0104
)

// FlashpkgVendor is the export name of the command-package LUN. init-flash.sh
// writes "<export_name><serial>" into the gadget's inquiry_string; listUMSDisks
// splits it back into the export name (UMSDisk.Vendor) and session serial via
// splitInquiry. "flashpkg" happens to be exactly 8 chars — most other export
// names (e.g. "mmcblk0") are not, which is why the naive 8-byte split fails.
const FlashpkgVendor = "flashpkg"

// sessionSerialLen is the length of the hex session id init-flash.sh appends to
// every export name in the gadget's inquiry_string.
const sessionSerialLen = 8

// splitInquiry recovers a LUN's export name and session serial from the SCSI
// INQUIRY Vendor + Product Identification fields. The gadget's inquiry_string
// is "<export_name><serial>", but the INQUIRY response splits it at a fixed
// 8-byte Vendor / 16-byte Product boundary — so an export name that isn't
// exactly 8 chars (e.g. "mmcblk0") straddles the two fields, and the serial's
// leading chars land in the Vendor field. Rejoin the fields and peel the
// trailing serial off to recover both. A combined string too short to hold a
// serial (a non-gadget disk) is returned unsplit.
func splitInquiry(vendor, product string) (name, serial string) {
	combined := strings.TrimSpace(vendor) + strings.TrimSpace(product)
	if len(combined) <= sessionSerialLen {
		return combined, ""
	}
	return combined[:len(combined)-sessionSerialLen], combined[len(combined)-sessionSerialLen:]
}

// UMSDisk is one USB mass-storage LUN the flashing initrd exposed.
type UMSDisk struct {
	DevPath   string // e.g. /dev/disk4 or /dev/sdb
	RawPath   string // e.g. /dev/rdisk4 (same as DevPath on Linux)
	SizeBytes int64
	Vendor    string // SCSI inquiry vendor, e.g. "flashpkg" or "mmcblk0"
	Serial    string // SCSI inquiry product: the device's 8-hex session id
}

// observedUMSHint formats the raw SCSI INQUIRY strings of every USB
// mass-storage LUN currently visible, for diagnosing a wait that timed out. It
// reports the vendor/product fields verbatim (before splitInquiry rejoins them)
// plus the BSD/block name, so a device advertising an unexpected export name —
// or a LUN the host never assigned a whole-disk node to — is obvious.
func observedUMSHint() string {
	raw := strings.TrimRight(rawUMSInquiry(), "\n")
	if raw == "" {
		return "No USB mass-storage LUNs are currently visible to this computer."
	}
	return "USB mass-storage LUNs currently visible (raw SCSI INQUIRY):\n" + raw
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
				return UMSDisk{}, fmt.Errorf("timed out waiting for USB storage %q (last scan error: %v)\n%s", vendor, err, observedUMSHint())
			}
			return UMSDisk{}, fmt.Errorf("timed out waiting for USB storage %q from the device\n%s", vendor, observedUMSHint())
		}
		select {
		case <-ctx.Done():
			return UMSDisk{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
