//go:build linux

package t234

import (
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
		d := UMSDisk{
			DevPath: "/dev/" + name,
			RawPath: "/dev/" + name,
			Vendor:  vendor,
			Serial:  sysfsString(filepath.Join(e, "device", "model")),
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
