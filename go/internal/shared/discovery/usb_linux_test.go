//go:build linux

package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUSBDeviceFromSysfs(t *testing.T) {
	tests := []struct {
		name            string
		vid, pid        string
		mfr, product    string
		wantOK          bool
		wantName        string
		wantDisplayName string
		wantESP32       bool
		wantVendorID    string
	}{
		{
			name:            "esp32 by vid:pid with no strings",
			vid:             "303a",
			pid:             "1001",
			wantOK:          true,
			wantName:        "ESP32-C6",
			wantDisplayName: "ESP32-C6",
			wantESP32:       true,
			wantVendorID:    "0x303a",
		},
		{
			name:            "esp32 vid uppercased by sysfs is still matched",
			vid:             "303A",
			pid:             "1001",
			wantOK:          true,
			wantName:        "ESP32-C6",
			wantDisplayName: "ESP32-C6",
			wantESP32:       true,
			wantVendorID:    "0x303a",
		},
		{
			name:            "wendy device by name",
			vid:             "1234",
			pid:             "5678",
			mfr:             "Wendy Labs",
			product:         "Edge Device",
			wantOK:          true,
			wantName:        "Wendy Labs Edge Device",
			wantDisplayName: "Wendy Labs Edge Device",
			wantESP32:       false,
			wantVendorID:    "0x1234",
		},
		{
			name:    "unrelated device is filtered out",
			vid:     "8086",
			pid:     "0001",
			mfr:     "Intel",
			product: "Hub",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dev, ok := usbDeviceFromSysfs(tt.vid, tt.pid, tt.mfr, tt.product)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if dev.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", dev.Name, tt.wantName)
			}
			if dev.DisplayName != tt.wantDisplayName {
				t.Errorf("DisplayName = %q, want %q", dev.DisplayName, tt.wantDisplayName)
			}
			if dev.IsESP32 != tt.wantESP32 {
				t.Errorf("IsESP32 = %v, want %v", dev.IsESP32, tt.wantESP32)
			}
			if !dev.IsWendyDevice {
				t.Errorf("IsWendyDevice = false, want true")
			}
			if dev.VendorID != tt.wantVendorID {
				t.Errorf("VendorID = %q, want %q", dev.VendorID, tt.wantVendorID)
			}
		})
	}
}

func TestDiscoverUSB_FromSysfsTree(t *testing.T) {
	root := t.TempDir()

	// Helper to create a fake sysfs device directory with the given attributes.
	mkdev := func(name string, attrs map[string]string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for k, v := range attrs {
			if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// An ESP32-C6 whole-device directory.
	mkdev("3-1", map[string]string{"idVendor": "303a", "idProduct": "1001"})
	// An unrelated device that must be ignored.
	mkdev("3-2", map[string]string{"idVendor": "8086", "idProduct": "0001", "manufacturer": "Intel"})
	// An interface directory for the ESP32 — must be skipped (has a colon, no ids).
	mkdev("3-1:1.0", map[string]string{})
	// A bus root without idVendor/idProduct — must be skipped.
	mkdev("usb3", map[string]string{})

	devices, err := discoverUSBAt(root)
	if err != nil {
		t.Fatalf("discoverUSBAt: %v", err)
	}

	if len(devices) != 1 {
		t.Fatalf("expected exactly 1 matched device, got %d: %+v", len(devices), devices)
	}
	if !devices[0].IsESP32 {
		t.Errorf("expected the matched device to be the ESP32, got %+v", devices[0])
	}
}
