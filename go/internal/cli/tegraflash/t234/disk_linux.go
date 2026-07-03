//go:build linux

package t234

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// listUMSDisks finds USB mass-storage whole disks via sysfs: the SCSI
// inquiry vendor/model land in /sys/block/sdX/device/{vendor,model}.
func listUMSDisks() ([]UMSDisk, error) {
	entries, err := filepath.Glob("/sys/block/sd*")
	if err != nil {
		return nil, err
	}
	var disks []UMSDisk
	for _, e := range entries {
		name := filepath.Base(e)
		vendor := sysfsString(filepath.Join(e, "device", "vendor"))
		if vendor == "" {
			continue
		}
		exportName, serial := splitInquiry(vendor, sysfsString(filepath.Join(e, "device", "model")))
		d := UMSDisk{
			DevPath: "/dev/" + name,
			RawPath: "/dev/" + name,
			Vendor:  exportName,
			Serial:  serial,
		}
		if sectors := sysfsString(filepath.Join(e, "size")); sectors != "" {
			if n, err := strconv.ParseInt(sectors, 10, 64); err == nil {
				d.SizeBytes = n * 512
			}
		}
		disks = append(disks, d)
	}
	return disks, nil
}

// rawUMSInquiry lists every /sys/block/sd* device's raw SCSI vendor/model — a
// diagnostic for a wait that timed out, showing what the device actually
// advertised before splitInquiry rejoins the fields.
func rawUMSInquiry() string {
	entries, err := filepath.Glob("/sys/block/sd*")
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		vendor := sysfsString(filepath.Join(e, "device", "vendor"))
		if vendor == "" {
			continue
		}
		var sizeBytes int64
		if sectors := sysfsString(filepath.Join(e, "size")); sectors != "" {
			if n, perr := strconv.ParseInt(sectors, 10, 64); perr == nil {
				sizeBytes = n * 512
			}
		}
		fmt.Fprintf(&b, "  - vendor=%q model=%q dev=%q size=%d\n",
			vendor, sysfsString(filepath.Join(e, "device", "model")), filepath.Base(e), sizeBytes)
	}
	return b.String()
}

// tegraUSBHint reports which Tegra-relevant USB devices are present (from
// sysfs), so a timed-out stage-2 wait can distinguish a board that rebooted
// into recovery from one still exposing the flashing gadget or gone from USB.
func tegraUSBHint() string {
	entries, _ := filepath.Glob("/sys/bus/usb/devices/*/idVendor")
	var found []string
	for _, ve := range entries {
		dir := filepath.Dir(ve)
		v, verr := strconv.ParseUint(sysfsString(ve), 16, 16)
		p, perr := strconv.ParseUint(sysfsString(filepath.Join(dir, "idProduct")), 16, 16)
		if verr != nil || perr != nil {
			continue
		}
		if label := tegraUSBLabel(uint16(v), uint16(p)); label != "" {
			found = append(found, label)
		}
	}
	if len(found) == 0 {
		return "No NVIDIA recovery (0955:*) or flashing-gadget (1d6b:0104) USB device is present — the board has left USB."
	}
	return "Tegra USB devices present: " + strings.Join(found, ", ")
}

func sysfsString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// unmountUMSDisk unmounts anything an automounter grabbed from the LUN.
// Best-effort: the LUNs usually carry no mountable filesystem.
func unmountUMSDisk(d UMSDisk) {
	exec.Command("umount", d.DevPath).Run() //nolint:errcheck
}
