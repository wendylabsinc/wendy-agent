//go:build linux

package services

import (
	"os"
	"path/filepath"
	"strings"
)

// usbPresentDevices scans sysfs for the vendor:product ids currently on the
// USB bus. Keys are "vvvv:pppp" (lowercase hex), matching
// appconfig.USBDeviceMatcher.String().
func usbPresentDevices() (map[string]bool, error) {
	const usbSysfsPath = "/sys/bus/usb/devices"
	entries, err := os.ReadDir(usbSysfsPath)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		devDir := filepath.Join(usbSysfsPath, entry.Name())
		vendor, err := os.ReadFile(filepath.Join(devDir, "idVendor"))
		if err != nil {
			continue
		}
		product, err := os.ReadFile(filepath.Join(devDir, "idProduct"))
		if err != nil {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(string(vendor)))
		p := strings.ToLower(strings.TrimSpace(string(product)))
		if v == "" || p == "" {
			continue
		}
		present[v+":"+p] = true
	}
	return present, nil
}
