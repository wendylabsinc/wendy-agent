//go:build windows

package commands

import (
	"os/exec"
	"strings"
	"sync"
)

var windowsWirelessInterfaces = sync.OnceValue(func() map[string]bool {
	out, err := exec.Command("netsh", "wlan", "show", "interfaces").Output()
	if err != nil {
		return nil
	}
	result := map[string]bool{}
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Name") {
			result[strings.ToLower(strings.TrimSpace(value))] = true
		}
	}
	return result
})

func networkInterfaceRoutePreference(name string) routePreference {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return routeUnknown
	}
	if windowsWirelessInterfaces()[name] {
		return routeWireless
	}
	return routeWired
}
