//go:build darwin

package commands

import "testing"

func TestParseDarwinInterfaceRoutePreferences(t *testing.T) {
	ports := `Hardware Port: USB 10/100/1000 LAN
Device: en7
Ethernet Address: aa:bb:cc:dd:ee:ff

Hardware Port: Wi-Fi
Device: en0
Ethernet Address: 11:22:33:44:55:66
`
	got := parseDarwinInterfaceRoutePreferences(ports)
	if got["en7"] != routeWired {
		t.Fatalf("en7 preference = %v, want wired", got["en7"])
	}
	if got["en0"] != routeWireless {
		t.Fatalf("en0 preference = %v, want wireless", got["en0"])
	}
}
