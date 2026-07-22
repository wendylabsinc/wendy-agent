package mesh

import (
	"net/netip"
	"testing"
)

func TestVIPForDevice(t *testing.T) {
	cases := []struct {
		id      int32
		want    string
		wantErr bool
	}{
		{215, "10.99.0.215", false},
		{1, "10.99.0.1", false},
		{256, "10.99.1.0", false},
		{65534, "10.99.255.254", false},
		{0, "", true},
		{65535, "", true},
		{-1, "", true},
	}
	for _, c := range cases {
		got, err := VIPForDevice(c.id)
		if c.wantErr != (err != nil) {
			t.Fatalf("VIPForDevice(%d): err = %v, wantErr %v", c.id, err, c.wantErr)
		}
		if err == nil && got.String() != c.want {
			t.Fatalf("VIPForDevice(%d) = %s, want %s", c.id, got, c.want)
		}
	}
}

func TestDeviceForVIP(t *testing.T) {
	cases := []struct {
		vip     string
		want    int32
		wantErr bool
	}{
		{"10.99.0.215", 215, false},
		{"10.99.255.254", 65534, false},
		{"10.99.0.0", 0, true},     // ID 0 invalid
		{"10.99.255.255", 0, true}, // ID 65535 invalid
		{"10.98.0.5", 0, true},     // outside CIDR
		{"192.168.1.1", 0, true},
	}
	for _, c := range cases {
		got, err := DeviceForVIP(netip.MustParseAddr(c.vip))
		if c.wantErr != (err != nil) {
			t.Fatalf("DeviceForVIP(%s): err = %v, wantErr %v", c.vip, err, c.wantErr)
		}
		if err == nil && got != c.want {
			t.Fatalf("DeviceForVIP(%s) = %d, want %d", c.vip, got, c.want)
		}
	}
}

func TestVIPRoundTrip(t *testing.T) {
	for _, id := range []int32{1, 255, 256, 4097, 65534} {
		vip, err := VIPForDevice(id)
		if err != nil {
			t.Fatalf("VIPForDevice(%d): %v", id, err)
		}
		back, err := DeviceForVIP(vip)
		if err != nil {
			t.Fatalf("DeviceForVIP(%s): %v", vip, err)
		}
		if back != id {
			t.Fatalf("round trip %d → %s → %d", id, vip, back)
		}
	}
}
