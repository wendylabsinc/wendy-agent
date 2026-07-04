//go:build linux

package discovery

import (
	"path/filepath"
	"strings"
)

// netClassRoot is the sysfs directory that enumerates network interfaces. It is
// a var so tests can point it at a fixture tree.
var netClassRoot = "/sys/class/net"

// interfaceIsUSBBacked reports whether the named network interface is backed by
// a USB device. It resolves the interface's sysfs symlink to its real device
// path and checks whether that path traverses the USB bus (a "usb*" path
// component). This catches USB-CDC gadget interfaces even when predictable
// naming is disabled (net.ifnames=0) and the interface is a plain name like
// "eth0"/"usb0" that the name heuristics in looksLikeUSBConnection would miss.
func interfaceIsUSBBacked(name string) bool {
	if name == "" {
		return false
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(netClassRoot, name))
	if err != nil {
		return false
	}
	return pathHasUSBComponent(resolved)
}

// pathHasUSBComponent reports whether any path segment names a USB bus/device
// (sysfs uses components like "usb1", "usb2", ...).
func pathHasUSBComponent(p string) bool {
	for _, seg := range strings.Split(p, string(filepath.Separator)) {
		if strings.HasPrefix(seg, "usb") {
			return true
		}
	}
	return false
}
