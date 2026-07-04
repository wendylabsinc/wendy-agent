//go:build linux

package discovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// ResolveESP32SerialPorts returns all connected serial ports whose USB VID/PID
// match the ESP32 constants, along with each device node's modification time as
// a proxy for when the device was plugged in.
func ResolveESP32SerialPorts() ([]SerialPortInfo, error) {
	entries, err := filepath.Glob("/sys/class/tty/ttyACM*")
	if err != nil {
		return nil, fmt.Errorf("globbing tty entries: %w", err)
	}

	wantVID := strings.TrimPrefix(models.ESP32VendorID, "0x")
	wantPID := strings.TrimPrefix(models.ESP32ProductID, "0x")

	var result []SerialPortInfo
	for _, entry := range entries {
		deviceSymlink := filepath.Join(entry, "device")
		resolvedIface, err := filepath.EvalSymlinks(deviceSymlink)
		if err != nil {
			continue
		}
		// Constrain resolved path to sysfs to prevent traversal via adversarial symlinks.
		if !strings.HasPrefix(resolvedIface, "/sys/devices/") {
			continue
		}
		usbDevPath := filepath.Dir(resolvedIface)

		vid, err := os.ReadFile(filepath.Join(usbDevPath, "idVendor"))
		if err != nil {
			continue
		}
		pid, err := os.ReadFile(filepath.Join(usbDevPath, "idProduct"))
		if err != nil {
			continue
		}

		if strings.TrimSpace(string(vid)) != wantVID || strings.TrimSpace(string(pid)) != wantPID {
			continue
		}

		devPath := "/dev/" + filepath.Base(entry)
		dev := SerialPortInfo{Port: devPath}
		if info, statErr := os.Stat(devPath); statErr == nil {
			dev.ConnectionTime = info.ModTime()
		}
		result = append(result, dev)
	}
	return result, nil
}
