package hardware

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/camera"
)

func TestSystemHardwareDiscoverer_Discover(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)

	caps, err := d.Discover(context.Background(), "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// On macOS, most Linux sysfs paths won't exist, so we may get zero results.
	// The test verifies that the function runs without error.
	t.Logf("Discovered %d hardware capabilities", len(caps))
}

func TestSystemHardwareDiscoverer_CategoryFilter(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)

	// Request only "gpu" category.
	caps, err := d.Discover(context.Background(), "gpu")
	if err != nil {
		t.Fatalf("Discover with filter: %v", err)
	}

	// Verify all returned capabilities are in the "gpu" category.
	for _, cap := range caps {
		if cap.Category != "gpu" {
			t.Errorf("expected category gpu, got %q", cap.Category)
		}
	}
}

func TestSystemHardwareDiscoverer_UnknownCategory(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)

	caps, err := d.Discover(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Discover with unknown filter: %v", err)
	}

	if len(caps) != 0 {
		t.Errorf("expected 0 results for unknown category, got %d", len(caps))
	}
}

func TestSystemHardwareDiscoverer_AllCategories(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)

	categories := []string{"gpu", "usb", "i2c", "spi", "gpio", "camera", "audio", "network", "storage"}
	for _, cat := range categories {
		caps, err := d.Discover(context.Background(), cat)
		if err != nil {
			t.Errorf("Discover(%q): %v", cat, err)
			continue
		}
		for _, cap := range caps {
			if cap.Category != cat {
				t.Errorf("category %q: got capability with category %q", cat, cap.Category)
			}
		}
		t.Logf("  %s: %d capabilities", cat, len(caps))
	}
}

func TestDiscoverCamera_TransportPropertyUSB(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)
	// Force classifier to return USB for any base name.
	d.classifyTransport = func(base string) (camera.Transport, string) {
		return camera.TransportUSB, "uvcvideo"
	}
	d.enumerateLibcamera = func(context.Context) (map[string]string, error) { return nil, nil }

	// We can't fabricate /dev/video* nodes in tests, so call discoverCamera
	// directly on whatever the host happens to have. If the host has none,
	// the assertions only run when caps != nil.
	caps := d.discoverCamera(context.Background())
	for _, c := range caps {
		if got := c.GetProperties()["transport"]; got != "usb" {
			t.Errorf("expected transport=usb, got %q", got)
		}
		if got := c.GetProperties()["driver"]; got != "uvcvideo" {
			t.Errorf("expected driver=uvcvideo, got %q", got)
		}
	}
}

func TestDiscoverCamera_TransportPropertyCSI_WithLibcameraID(t *testing.T) {
	logger := zap.NewNop()
	d := NewSystemHardwareDiscoverer(logger)
	d.classifyTransport = func(base string) (camera.Transport, string) {
		return camera.TransportCSI, "tegra-capture-vi"
	}
	d.enumerateLibcamera = func(context.Context) (map[string]string, error) {
		return map[string]string{"/base/cam@1a": "Sensor"}, nil
	}

	caps := d.discoverCamera(context.Background())
	if len(caps) == 0 {
		t.Skip("no /dev/video* on this host; CSI assertions skipped")
	}
	if len(caps) > 1 {
		// Ambiguous case — id must remain unset on every cap.
		for _, c := range caps {
			if _, ok := c.GetProperties()["libcamera_id"]; ok {
				t.Errorf("ambiguous mapping must not populate libcamera_id: %v", c.GetProperties())
			}
		}
		return
	}
	if got := caps[0].GetProperties()["transport"]; got != "csi" {
		t.Errorf("expected transport=csi, got %q", got)
	}
	if got := caps[0].GetProperties()["libcamera_id"]; got != "/base/cam@1a" {
		t.Errorf("expected libcamera_id=/base/cam@1a, got %q", got)
	}
}

func writeUSBSysfsDevice(t *testing.T, root, name string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, content := range attrs {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiscoverUSB_IdentityProperties(t *testing.T) {
	root := t.TempDir()
	writeUSBSysfsDevice(t, root, "1-2.4", map[string]string{
		"product":      "CANable2",
		"idVendor":     "16d0",
		"idProduct":    "117e",
		"manufacturer": "Openlight Labs",
		"serial":       "004E00265548501120343041",
		"busnum":       "1",
		"devnum":       "9",
		"speed":        "12",
	})
	// Interface entries (1-2.4:1.0) and devices without a product string are skipped.
	writeUSBSysfsDevice(t, root, "1-2.4:1.0", map[string]string{"bInterfaceClass": "02"})
	writeUSBSysfsDevice(t, root, "2-1", map[string]string{"idVendor": "1d6b", "idProduct": "0003"})

	d := NewSystemHardwareDiscoverer(zap.NewNop())
	d.usbSysfsPath = root

	caps := d.discoverUSB()
	if len(caps) != 1 {
		t.Fatalf("expected 1 usb capability, got %d: %v", len(caps), caps)
	}
	c := caps[0]
	if c.GetDescription() != "CANable2 (16d0:117e)" {
		t.Errorf("unexpected description %q", c.GetDescription())
	}
	want := map[string]string{
		"vendor_id":    "16d0",
		"product_id":   "117e",
		"manufacturer": "Openlight Labs",
		"serial":       "004E00265548501120343041",
		"busnum":       "1",
		"devnum":       "9",
		"speed_mbps":   "12",
		"port_path":    "1-2.4",
	}
	for k, v := range want {
		if got := c.GetProperties()[k]; got != v {
			t.Errorf("properties[%q] = %q, want %q", k, got, v)
		}
	}
}

func TestDiscoverUSB_MissingOptionalAttrs(t *testing.T) {
	root := t.TempDir()
	writeUSBSysfsDevice(t, root, "3-1", map[string]string{
		"product":   "Widget",
		"idVendor":  "dead",
		"idProduct": "beef",
	})

	d := NewSystemHardwareDiscoverer(zap.NewNop())
	d.usbSysfsPath = root

	caps := d.discoverUSB()
	if len(caps) != 1 {
		t.Fatalf("expected 1 usb capability, got %d", len(caps))
	}
	props := caps[0].GetProperties()
	for _, absent := range []string{"serial", "manufacturer", "busnum", "devnum", "speed_mbps"} {
		if v, ok := props[absent]; ok {
			t.Errorf("expected %q to be absent, got %q", absent, v)
		}
	}
	if props["vendor_id"] != "dead" || props["product_id"] != "beef" || props["port_path"] != "3-1" {
		t.Errorf("unexpected identity properties: %v", props)
	}
}
