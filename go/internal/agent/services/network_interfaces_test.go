package services

import (
	"net"
	"testing"
)

func TestCollectNetworkInterfaces(t *testing.T) {
	views := []ifaceView{
		// Normal wired interface — kept.
		{
			name:  "eth0",
			isUp:  true,
			addrs: []net.IP{net.ParseIP("192.168.1.42"), net.ParseIP("fe80::1")},
		},
		// Wireless interface with an IPv6 global address — kept.
		{
			name:  "wlan0",
			isUp:  true,
			addrs: []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("2001:db8::1")},
		},
		// Loopback — dropped.
		{
			name:       "lo",
			isUp:       true,
			isLoopback: true,
			addrs:      []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		},
		// Down interface — dropped even though it has an address.
		{
			name:  "eth1",
			isUp:  false,
			addrs: []net.IP{net.ParseIP("192.168.5.1")},
		},
		// Container bridge — dropped by name.
		{
			name:  "docker0",
			isUp:  true,
			addrs: []net.IP{net.ParseIP("172.17.0.1")},
		},
		// CNI veth pair — dropped by name.
		{
			name:  "veth1234",
			isUp:  true,
			addrs: []net.IP{net.ParseIP("10.88.0.1")},
		},
		// Up, real interface but only link-local addresses — dropped (no routable IP).
		{
			name:  "eth2",
			isUp:  true,
			addrs: []net.IP{net.ParseIP("169.254.1.1"), net.ParseIP("fe80::2")},
		},
	}

	got := collectNetworkInterfaces(views)

	if len(got) != 2 {
		t.Fatalf("collectNetworkInterfaces returned %d interfaces, want 2: %+v", len(got), got)
	}
	if got[0].GetName() != "eth0" {
		t.Errorf("got[0].Name = %q, want eth0", got[0].GetName())
	}
	if len(got[0].GetIpAddresses()) != 1 || got[0].GetIpAddresses()[0] != "192.168.1.42" {
		t.Errorf("eth0 addresses = %v, want [192.168.1.42] (link-local excluded)", got[0].GetIpAddresses())
	}
	if got[1].GetName() != "wlan0" {
		t.Errorf("got[1].Name = %q, want wlan0", got[1].GetName())
	}
	if len(got[1].GetIpAddresses()) != 2 {
		t.Errorf("wlan0 addresses = %v, want both v4 and v6", got[1].GetIpAddresses())
	}
}

func TestCollectNetworkInterfacesEmpty(t *testing.T) {
	if got := collectNetworkInterfaces(nil); got != nil {
		t.Errorf("collectNetworkInterfaces(nil) = %v, want nil", got)
	}
}

func TestIsVirtualIface(t *testing.T) {
	cases := map[string]bool{
		"eth0":      false,
		"wlan0":     false,
		"en0":       false,
		"docker0":   true,
		"veth9a8b":  true,
		"cni0":      true,
		"flannel.1": true,
		"br-abc123": true,
		"virbr0":    true,
		"nerdctl0":  true,
	}
	for name, want := range cases {
		if got := isVirtualIface(name); got != want {
			t.Errorf("isVirtualIface(%q) = %v, want %v", name, got, want)
		}
	}
}
