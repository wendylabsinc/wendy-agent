//go:build darwin || linux || windows

package t234

import "testing"

func TestSplitInquiry(t *testing.T) {
	cases := []struct {
		vendor, product  string
		wantName, wantID string
	}{
		{"flashpkg", "8e81a60b", "flashpkg", "8e81a60b"},
		{"mmcblk08", "e81a60b", "mmcblk0", "8e81a60b"},
		{"nvme0n18", "e81a60b   ", "nvme0n1", "8e81a60b"},
		{"flashpkg", "", "flashpkg", ""},
	}
	for _, tc := range cases {
		name, id := splitInquiry(tc.vendor, tc.product)
		if name != tc.wantName || id != tc.wantID {
			t.Errorf("splitInquiry(%q, %q) = (%q, %q), want (%q, %q)", tc.vendor, tc.product, name, id, tc.wantName, tc.wantID)
		}
	}
}
