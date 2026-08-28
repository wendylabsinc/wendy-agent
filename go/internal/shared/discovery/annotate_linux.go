//go:build linux

package discovery

import (
	"context"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// init overrides stream.go's no-op newLANAnnotator default with the linux
// refinement: sysfs link speed via setLANNetworkInterface, the same
// annotation the pre-StreamLAN batch discoverLAN applied to every resolved
// device before that code was deleted once CollectLAN/StreamLAN took over
// LAN discovery. Unlike darwin/windows, Linux has no interface display-name
// source, so only the link speed is supplied; setLANNetworkInterface itself
// derives NetworkInterface and the USB summary from the interface name.
func init() {
	newLANAnnotator = func(ctx context.Context) func(*models.LANDevice) {
		linkSpeeds := make(map[string]string)
		return func(dev *models.LANDevice) {
			setLANNetworkInterface(dev, dev.NetworkInterface, "", cachedLinuxInterfaceLinkSpeed(dev.NetworkInterface, linkSpeeds))
		}
	}
}

// linuxInterfaceLinkSpeedFn is the sysfs read cachedLinuxInterfaceLinkSpeed
// calls. A var, not a direct linuxInterfaceLinkSpeed call, so a test can pin
// it to a fake and assert the memoization below without depending on a real
// /sys/class/net entry existing for a test interface name.
var linuxInterfaceLinkSpeedFn = linuxInterfaceLinkSpeed

// cachedLinuxInterfaceLinkSpeed memoizes linuxInterfaceLinkSpeedFn (a sysfs
// read under /sys/class/net) per interface for one discovery session,
// mirroring darwin's darwinCachedInterfaceLinkSpeed.
func cachedLinuxInterfaceLinkSpeed(interfaceName string, linkSpeeds map[string]string) string {
	if interfaceName == "" {
		return ""
	}
	if linkSpeed, ok := linkSpeeds[interfaceName]; ok {
		return linkSpeed
	}
	linkSpeed := linuxInterfaceLinkSpeedFn(interfaceName)
	linkSpeeds[interfaceName] = linkSpeed
	return linkSpeed
}
