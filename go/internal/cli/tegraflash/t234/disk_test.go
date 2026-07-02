//go:build darwin || linux

package t234

import "testing"

// TestSplitInquiry covers the INQUIRY re-joining that recovers a LUN's export
// name and session serial. The flashing gadget's inquiry_string is
// "<export_name><serial>" (8-hex serial), but the SCSI response splits it at a
// fixed 8-byte Vendor / 16-byte Product boundary — so any export name that is
// not exactly 8 chars straddles the two fields.
func TestSplitInquiry(t *testing.T) {
	cases := []struct {
		desc            string
		vendor, product string
		wantName        string
		wantSerial      string
	}{
		{"8-char name splits cleanly", "flashpkg", "8e81a60b", "flashpkg", "8e81a60b"},
		{"7-char name straddles the boundary", "mmcblk08", "e81a60b", "mmcblk0", "8e81a60b"},
		{"short name straddles the boundary", "sda8e81a", "60b", "sda", "8e81a60b"},
		{"trailing space padding trimmed", "mmcblk08", "e81a60b   ", "mmcblk0", "8e81a60b"},
		{"too short to hold a serial", "flashpkg", "", "flashpkg", ""},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			name, serial := splitInquiry(c.vendor, c.product)
			if name != c.wantName {
				t.Errorf("name = %q, want %q", name, c.wantName)
			}
			if serial != c.wantSerial {
				t.Errorf("serial = %q, want %q", serial, c.wantSerial)
			}
		})
	}
}
