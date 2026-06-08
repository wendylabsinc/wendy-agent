package hardware

import (
	"context"
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
