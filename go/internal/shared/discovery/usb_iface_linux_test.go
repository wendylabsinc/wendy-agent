//go:build linux

package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

// buildNetFixture creates a fake /sys/class/net tree where each interface name
// is a symlink to a device directory under devicesRoot. It returns the path to
// use as netClassRoot.
func buildNetFixture(t *testing.T, ifaces map[string]string) string {
	t.Helper()
	base := t.TempDir()
	classRoot := filepath.Join(base, "class", "net")
	if err := os.MkdirAll(classRoot, 0o755); err != nil {
		t.Fatalf("mkdir class root: %v", err)
	}
	for iface, devRel := range ifaces {
		devAbs := filepath.Join(base, devRel)
		if err := os.MkdirAll(devAbs, 0o755); err != nil {
			t.Fatalf("mkdir device dir %s: %v", devAbs, err)
		}
		if err := os.Symlink(devAbs, filepath.Join(classRoot, iface)); err != nil {
			t.Fatalf("symlink %s: %v", iface, err)
		}
	}
	return classRoot
}

func TestInterfaceIsUSBBacked(t *testing.T) {
	orig := netClassRoot
	defer func() { netClassRoot = orig }()

	netClassRoot = buildNetFixture(t, map[string]string{
		// USB-CDC gadget: device path traverses a usb1 bus component.
		"usb0": "devices/platform/soc/usb1/1-1/1-1:1.0/net/usb0",
		// Predictable-name gadget, same USB topology.
		"enp0s20u1": "devices/pci0000:00/0000:00:14.0/usb2/2-1/2-1:1.0/net/enp0s20u1",
		// On-board PCI ethernet: no usb component.
		"eth0": "devices/pci0000:00/0000:00:1f.6/net/eth0",
	})

	tests := []struct {
		iface string
		want  bool
	}{
		{"usb0", true},
		{"enp0s20u1", true},
		{"eth0", false},
		{"missing0", false}, // no such interface
		{"", false},
	}
	for _, tt := range tests {
		if got := interfaceIsUSBBacked(tt.iface); got != tt.want {
			t.Errorf("interfaceIsUSBBacked(%q) = %v, want %v", tt.iface, got, tt.want)
		}
	}
}

// looksLikeUSBConnection must classify a USB-backed interface as USB even when
// its name (classic eth0 under net.ifnames=0) matches none of the name rules.
func TestLooksLikeUSBConnection_SysfsFallback(t *testing.T) {
	orig := netClassRoot
	defer func() { netClassRoot = orig }()
	netClassRoot = buildNetFixture(t, map[string]string{
		"eth0": "devices/platform/soc/usb1/1-1/1-1:1.0/net/eth0",
		"eth1": "devices/pci0000:00/0000:00:1f.6/net/eth1",
	})

	if !looksLikeUSBConnection("eth0", "") {
		t.Error("expected USB-backed eth0 to be detected as a USB connection")
	}
	if looksLikeUSBConnection("eth1", "") {
		t.Error("did not expect on-board eth1 to be detected as a USB connection")
	}
}
