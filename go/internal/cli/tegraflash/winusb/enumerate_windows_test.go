//go:build windows

package winusb

import "testing"

// These helpers are shared with package t234's gadget-disk discovery and USB
// release; both sides must extract identity from PnP instance IDs identically.
func TestParseVIDPID(t *testing.T) {
	vid, pid, ok := ParseVIDPID(`USB\VID_0955&PID_7023\1421322044837`)
	if !ok || vid != 0x0955 || pid != 0x7023 {
		t.Fatalf("got %04x:%04x ok=%v", vid, pid, ok)
	}
	// usbccgp function devnodes carry the same VID/PID with an MI_ suffix.
	vid, pid, ok = ParseVIDPID(`USB\VID_1D6B&PID_0104&MI_00\7&2C54F607&0&0000`)
	if !ok || vid != 0x1d6b || pid != 0x0104 {
		t.Fatalf("MI node: got %04x:%04x ok=%v", vid, pid, ok)
	}
	if _, _, ok := ParseVIDPID(`USBSTOR\DISK&VEN_FLASHPKG&PROD_12AB34CD\7&AB`); ok {
		t.Fatal("USBSTOR id has no VID_/PID_ and must not parse")
	}
}

func TestInstanceSerial(t *testing.T) {
	if got := InstanceSerial(`USB\VID_1D6B&PID_0104\F3885343`); got != "F3885343" {
		t.Fatalf("serial = %q", got)
	}
	// Windows-generated ID for a device without an iSerial: returned as-is.
	if got := InstanceSerial(`USB\VID_1D6B&PID_0104&MI_00\7&2C54F607&0&0000`); got != "7&2C54F607&0&0000" {
		t.Fatalf("generated id = %q", got)
	}
	if got := InstanceSerial("no-backslash"); got != "" {
		t.Fatalf("malformed id = %q", got)
	}
}
