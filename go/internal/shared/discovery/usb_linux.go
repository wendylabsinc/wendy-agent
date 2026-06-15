//go:build linux

package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// defaultUSBSysfsRoot is the sysfs directory that enumerates USB devices.
const defaultUSBSysfsRoot = "/sys/bus/usb/devices"

// discoverUSB enumerates USB devices from sysfs and returns those that look like
// a Wendy device (by name) or an Espressif ESP32-C6 (by VID:PID). Reading sysfs
// directly avoids shelling out to lsusb and parsing its human-readable output.
func discoverUSB(_ context.Context) ([]models.USBDevice, error) {
	return discoverUSBAt(defaultUSBSysfsRoot)
}

// discoverUSBAt enumerates USB devices under an explicit sysfs root. The root is
// trusted configuration (the kernel-controlled sysfs path in production); tests
// pass a fixture tree directly rather than mutating shared state.
func discoverUSBAt(root string) ([]models.USBDevice, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var devices []models.USBDevice
	for _, entry := range entries {
		name := entry.Name()
		// Interface directories (e.g. "1-1:1.0") contain a colon and carry no
		// idVendor/idProduct; only whole-device directories are of interest.
		if strings.Contains(name, ":") {
			continue
		}
		// Defensive: os.ReadDir yields base names (never "." / ".." or a name
		// with a separator), but guard anyway so a redirected root can never be
		// escaped via filepath.Join.
		if name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) {
			continue
		}

		dir := filepath.Join(root, name)
		vid := readSysfsAttr(dir, "idVendor")
		pid := readSysfsAttr(dir, "idProduct")
		if vid == "" || pid == "" {
			continue
		}

		dev, ok := usbDeviceFromSysfs(vid, pid,
			readSysfsAttr(dir, "manufacturer"),
			readSysfsAttr(dir, "product"))
		if !ok {
			continue
		}
		devices = append(devices, dev)
	}

	return devices, nil
}

// readSysfsAttr reads a single sysfs attribute file, returning its trimmed
// contents or "" if the file is absent or unreadable.
func readSysfsAttr(dir, attr string) string {
	data, err := os.ReadFile(filepath.Join(dir, attr))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// usbDeviceFromSysfs builds a USBDevice from sysfs attribute values. vid and pid
// are hex strings without the "0x" prefix (as exposed by sysfs). It reports
// false when the device is neither a Wendy device (by name) nor an ESP32-C6 (by
// VID:PID), matching the filtering previously performed on lsusb output.
func usbDeviceFromSysfs(vid, pid, manufacturer, product string) (models.USBDevice, bool) {
	vid = strings.ToLower(vid)
	pid = strings.ToLower(pid)

	isESP32 := vid == strings.TrimPrefix(models.ESP32VendorID, "0x") &&
		pid == strings.TrimPrefix(models.ESP32ProductID, "0x")
	name := strings.TrimSpace(manufacturer + " " + product)
	isWendy := strings.Contains(strings.ToLower(name), "wendy")

	if !isWendy && !isESP32 {
		return models.USBDevice{}, false
	}

	dev := models.USBDevice{
		IsWendyDevice: isWendy || isESP32,
		IsESP32:       isESP32,
		VendorID:      "0x" + vid,
		ProductID:     "0x" + pid,
		Name:          name,
	}
	if dev.Name == "" {
		if isESP32 {
			dev.Name = "ESP32-C6"
		} else {
			dev.Name = "Wendy Device"
		}
	}
	dev.DisplayName = dev.Name
	if isESP32 {
		dev.DisplayName = "ESP32-C6"
	}

	return dev, true
}
