package adb

import "fmt"

const maxADBDevices = 32

// selectADBPath is kept platform-neutral so exact target selection can be
// exhaustively tested without opening a USB device or requiring libusb.
func selectADBPath(paths []string, wantPath string, requirePath bool) (index int, fellBack bool, err error) {
	if len(paths) > maxADBDevices {
		return -1, false, fmt.Errorf("refusing ADB discovery with %d devices (maximum %d)", len(paths), maxADBDevices)
	}
	if len(paths) == 0 {
		return -1, false, fmt.Errorf("no ADB devices are present")
	}
	if wantPath == "" {
		if requirePath {
			return -1, false, fmt.Errorf("an exact ADB controller USB path is required")
		}
		return 0, false, nil
	}
	match := -1
	for i, path := range paths {
		if path == wantPath {
			if match != -1 {
				return -1, false, fmt.Errorf("multiple ADB devices matched the required recovery USB path")
			}
			match = i
		}
	}
	if match != -1 {
		return match, false, nil
	}
	if len(paths) == 1 && !requirePath {
		return 0, true, nil
	}
	return -1, false, fmt.Errorf("no ADB device at the required recovery USB path among %d ADB devices present", len(paths))
}
