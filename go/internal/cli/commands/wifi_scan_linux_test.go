//go:build linux

package commands

import "testing"

func TestNmcliHasWifiDevice(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			"wifi device present",
			"lo:loopback\nenp3s0:ethernet\nwlan0:wifi\n",
			true,
		},
		{
			"no wifi device",
			"lo:loopback\nenp3s0:ethernet\n",
			false,
		},
		{"empty output", "", false},
		{
			"wifi-p2p does not count",
			"lo:loopback\np2p-dev-wlan0:wifi-p2p\n",
			false,
		},
		{
			"device name containing escaped colon",
			"weird\\:name:wifi\n",
			true,
		},
		{
			"unexpected third column does not match",
			"wlan0:wifi:connected\n",
			false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := nmcliHasWifiDevice(tc.output); got != tc.want {
				t.Fatalf("nmcliHasWifiDevice(%q) = %v; want %v", tc.output, got, tc.want)
			}
		})
	}
}
