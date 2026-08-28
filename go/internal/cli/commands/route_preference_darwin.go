//go:build darwin

package commands

import (
	"os/exec"
	"strings"
	"sync"
)

var darwinPortKinds = sync.OnceValue(func() map[string]routePreference {
	out, err := exec.Command("/usr/sbin/networksetup", "-listallhardwareports").Output()
	if err != nil {
		return nil
	}
	return parseDarwinInterfaceRoutePreferences(string(out))
})

var darwinBridgeKinds sync.Map

func parseDarwinInterfaceRoutePreferences(output string) map[string]routePreference {
	result := map[string]routePreference{}
	hardwarePort := ""
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Hardware Port:"):
			hardwarePort = strings.TrimSpace(strings.TrimPrefix(line, "Hardware Port:"))
		case strings.HasPrefix(line, "Device:"):
			name := strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			if name == "" {
				continue
			}
			label := strings.ToLower(hardwarePort)
			switch {
			case strings.Contains(label, "wi-fi"), strings.Contains(label, "wifi"), strings.Contains(label, "airport"):
				result[name] = routeWireless
			case strings.Contains(label, "ethernet"), strings.Contains(label, "lan"), strings.Contains(label, "thunderbolt"), strings.Contains(label, "usb"):
				result[name] = routeWired
			}
		}
	}
	return result
}

func networkInterfaceRoutePreference(name string) routePreference {
	name = strings.TrimSpace(name)
	if preference := darwinPortKinds()[name]; preference != routeUnknown {
		return preference
	}
	if !strings.HasPrefix(name, "bridge") {
		return routeUnknown
	}
	if cached, ok := darwinBridgeKinds.Load(name); ok {
		return cached.(routePreference)
	}
	preference := routeUnknown
	if out, err := exec.Command("/sbin/ifconfig", name).Output(); err == nil {
		for _, raw := range strings.Split(string(out), "\n") {
			fields := strings.Fields(strings.TrimSpace(raw))
			if len(fields) < 2 || fields[0] != "member:" {
				continue
			}
			if memberPreference := darwinPortKinds()[fields[1]]; memberPreference > preference {
				preference = memberPreference
			}
		}
	}
	darwinBridgeKinds.Store(name, preference)
	return preference
}
