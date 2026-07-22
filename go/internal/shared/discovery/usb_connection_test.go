package discovery

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestIsRoutableLANAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.1.10", true},                     // IPv4 private
		{"10.0.0.5", true},                         // IPv4 private
		{"169.254.1.1", true},                      // IPv4 link-local (APIPA) is still routable per "IPv4 (any)"
		{"2001:db8::1", true},                      // global IPv6
		{"fd00::1", true},                          // ULA IPv6 (not link-local)
		{"::1", true},                              // IPv6 loopback is not link-local-unicast
		{"fe80::1", false},                         // IPv6 link-local
		{"fe80::1dc5:4d23:df52:fc45%wlan0", false}, // zoned IPv6 link-local
		{"", false},                                // empty
		{"not-an-ip", false},                       // garbage
	}
	for _, tc := range cases {
		if got := isRoutableLANAddress(tc.addr); got != tc.want {
			t.Errorf("isRoutableLANAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestAppendPreferredLANDevicePrefersRoutable(t *testing.T) {
	v4 := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "192.168.1.5"}
	v6ll := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "fe80::1%wlan0"}
	const key = "cam-cam.local-50052"

	// Link-local discovered first, then routable IPv4 → IPv4 must win.
	var devs []models.LANDevice
	idx := map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.1.5" {
		t.Fatalf("routable IPv4 should win, got %+v", devs)
	}

	// Routable IPv4 first, then link-local → IPv4 must remain.
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.1.5" {
		t.Fatalf("routable IPv4 should remain, got %+v", devs)
	}

	// Only link-local available → it must be kept (no address dropped).
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6ll)
	if len(devs) != 1 || devs[0].IPAddress != "fe80::1%wlan0" {
		t.Fatalf("link-local should be kept when only option, got %+v", devs)
	}
}

func TestAppendPreferredLANDevicePrefersIPv4OverGlobalIPv6(t *testing.T) {
	// avahi resolves the same device once per protocol; the IPv6 entry often
	// carries a rotating RFC 4941 temporary address. IPv4 must win regardless
	// of arrival order — before this rule both addresses scored the same, so
	// whichever resolved first (usually IPv6) was kept.
	v4 := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "192.168.0.159"}
	v6 := models.LANDevice{ID: "d", DisplayName: "cam", Hostname: "cam.local", Port: 50052, IPAddress: "2600:1011:a003:4221:be41:6859:13c0:f7"}
	const key = "cam-cam.local-50052"

	// IPv6 discovered first, then IPv4 → IPv4 must win.
	var devs []models.LANDevice
	idx := map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6)
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.0.159" {
		t.Fatalf("IPv4 should win over global IPv6, got %+v", devs)
	}

	// IPv4 first, then IPv6 → IPv4 must remain, even when the IPv6 record
	// carries more metadata (which would otherwise out-score it).
	v6To := v6
	v6To.NetworkInterface = "wlan0"
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v4)
	devs = appendPreferredLANDevice(devs, idx, key, v6To)
	if len(devs) != 1 || devs[0].IPAddress != "192.168.0.159" {
		t.Fatalf("IPv4 should remain over global IPv6, got %+v", devs)
	}

	// Only IPv6 available → it must be kept.
	devs = nil
	idx = map[string]int{}
	devs = appendPreferredLANDevice(devs, idx, key, v6)
	if len(devs) != 1 || devs[0].IPAddress != v6.IPAddress {
		t.Fatalf("IPv6 should be kept when only option, got %+v", devs)
	}
}

func TestIsIPv4LANAddress(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.1.10", true},
		{"169.254.1.1", true},
		{"::ffff:192.168.1.10", true}, // IPv4-mapped
		{"2001:db8::1", false},
		{"fe80::1%wlan0", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, tc := range cases {
		if got := isIPv4LANAddress(tc.addr); got != tc.want {
			t.Errorf("isIPv4LANAddress(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
