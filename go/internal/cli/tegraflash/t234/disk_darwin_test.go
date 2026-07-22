//go:build darwin

package t234

import "testing"

func TestMacUSBPortPathMatchesLibusbTopology(t *testing.T) {
	// macOS locationID: bus 20, downstream ports 1 then 2.
	if got := macUSBPortPath(0x14120000); got != "20-1.2" {
		t.Fatalf("port key = %q, want 20-1.2", got)
	}
}
