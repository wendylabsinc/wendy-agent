//go:build linux

package commands

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

func networkInterfaceRoutePreference(name string) routePreference {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name {
		return routeUnknown
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagLoopback != 0 {
		return routeUnknown
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name, "wireless")); err == nil {
		return routeWireless
	}
	return routeWired
}
