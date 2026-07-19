//go:build linux

package services

import (
	"os"
	"path/filepath"
	"strings"
)

// usbPresentDetail scans sysfs for the devices currently on the USB bus,
// including serials so watches can pin one of two identical adapters.
func usbPresentDetail() ([]presentUSBDevice, error) {
	const usbSysfsPath = "/sys/bus/usb/devices"
	entries, err := os.ReadDir(usbSysfsPath)
	if err != nil {
		return nil, err
	}
	read := func(dir, name string) string {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	var present []presentUSBDevice
	for _, entry := range entries {
		devDir := filepath.Join(usbSysfsPath, entry.Name())
		vendor := strings.ToLower(read(devDir, "idVendor"))
		product := strings.ToLower(read(devDir, "idProduct"))
		if vendor == "" || product == "" {
			continue
		}
		present = append(present, presentUSBDevice{
			VendorID:  vendor,
			ProductID: product,
			Serial:    read(devDir, "serial"),
		})
	}
	return present, nil
}
