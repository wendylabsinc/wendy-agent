package discovery

import (
	"net"
	"testing"
)

func TestUSBDirectCandidateHostPort(t *testing.T) {
	c := USBDirectCandidate{Interface: "enxaabbccddeeff", Zone: "enxaabbccddeeff"}
	got := c.HostPort(50051)
	want := "[fe80::5741:1%enxaabbccddeeff]:50051"
	if got != want {
		t.Fatalf("HostPort = %q, want %q", got, want)
	}
}

func TestUSBDirectCandidatesFrom(t *testing.T) {
	ifaces := []net.Interface{
		{Index: 3, Name: "enxaabbccddeeff", Flags: net.FlagUp},            // USB by name → candidate
		{Index: 4, Name: "lo0", Flags: net.FlagUp | net.FlagLoopback},     // loopback → skipped
		{Index: 5, Name: "wlan1", Flags: net.FlagUp},                      // not USB → skipped
		{Index: 6, Name: "usb0", Flags: 0},                                // USB name but DOWN → skipped
		{Index: 7, Name: "ncm0", Flags: net.FlagUp},                       // NCM adapter → candidate
	}

	got := usbDirectCandidatesFrom(ifaces, "linux")
	want := []USBDirectCandidate{
		{Interface: "enxaabbccddeeff", Zone: "enxaabbccddeeff"},
		{Interface: "ncm0", Zone: "ncm0"},
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// Windows zones are numeric interface indexes, not names.
	gotWin := usbDirectCandidatesFrom(ifaces, "windows")
	if len(gotWin) != 2 || gotWin[0].Zone != "3" || gotWin[1].Zone != "7" {
		t.Fatalf("windows candidates = %+v, want zones \"3\" and \"7\"", gotWin)
	}
}
