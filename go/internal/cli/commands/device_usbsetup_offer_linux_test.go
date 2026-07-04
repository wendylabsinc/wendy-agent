//go:build linux

package commands

import (
	"net"
	"testing"
)

func TestIPv4Configured(t *testing.T) {
	cases := []struct {
		name  string
		addrs []net.Addr
		want  bool
	}{
		{"empty", nil, false},
		{"only ipv6 link-local", []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1")}}, false},
		{"ipv4 link-local", []net.Addr{&net.IPNet{IP: net.ParseIP("169.254.3.4")}}, true},
		{"ipv4 routable", []net.Addr{&net.IPNet{IP: net.ParseIP("10.42.0.5")}}, true},
		{"ipaddr form", []net.Addr{&net.IPAddr{IP: net.ParseIP("10.42.0.5")}}, true},
	}
	for _, c := range cases {
		if got := ipv4Configured(c.addrs); got != c.want {
			t.Errorf("%s: ipv4Configured = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestDetectUnconfiguredUSBGadget(t *testing.T) {
	origNames := usbGadgetIfaceNames
	origAddrs := usbIfaceAddrs
	origProfile := usbSetupProfileExists
	t.Cleanup(func() {
		usbGadgetIfaceNames = origNames
		usbIfaceAddrs = origAddrs
		usbSetupProfileExists = origProfile
	})
	usbSetupProfileExists = func() bool { return false }

	ipv6Only := []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1")}}
	withIPv4 := []net.Addr{&net.IPNet{IP: net.ParseIP("10.42.0.5")}}

	cases := []struct {
		name  string
		names []string
		addrs []net.Addr
		want  string
	}{
		{"no gadget", nil, nil, ""},
		{"ambiguous (two gadgets)", []string{"usb0", "usb1"}, ipv6Only, ""},
		{"unconfigured", []string{"enxaa"}, ipv6Only, "enxaa"},
		{"already configured", []string{"enxaa"}, withIPv4, ""},
	}
	for _, c := range cases {
		names, addrs := c.names, c.addrs
		usbGadgetIfaceNames = func() ([]string, error) { return names, nil }
		usbIfaceAddrs = func(string) ([]net.Addr, error) { return addrs, nil }
		if got := detectUnconfiguredUSBGadget(); got != c.want {
			t.Errorf("%s: detectUnconfiguredUSBGadget = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestResolveUSBSetupInterface_Override(t *testing.T) {
	got, err := resolveUSBSetupInterface("usb7")
	if err != nil || got != "usb7" {
		t.Fatalf("resolveUSBSetupInterface(override) = %q, %v; want usb7, nil", got, err)
	}
}
