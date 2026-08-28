//go:build darwin

package discovery

import (
	"context"
	"regexp"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// init overrides stream.go's no-op newLANAnnotator default with the darwin
// refinement: interface display names and link speeds, the same calls
// discoverLAN used to make (darwinInterfaceDisplayNameMap once per session,
// then per-device darwinCachedInterfaceLinkSpeed/setLANNetworkInterface).
// Safe against init-ordering: stream.go's default is a var initializer, and
// Go runs all of a program's var initializers before any package's init().
func init() {
	newLANAnnotator = func(ctx context.Context) func(*models.LANDevice) {
		interfaceDisplayNames := darwinInterfaceDisplayNameMap(ctx)
		linkSpeeds := make(map[string]string)
		return func(dev *models.LANDevice) {
			setLANNetworkInterface(dev, dev.NetworkInterface, interfaceDisplayNames[dev.NetworkInterface], darwinCachedInterfaceLinkSpeed(ctx, dev.NetworkInterface, linkSpeeds))
		}
	}
}

// browseResult identifies one mDNS browse answer: the instance and domain
// dns_sd.h reported, plus the interface it arrived on.
type browseResult struct {
	instanceName   string
	domain         string
	interfaceName  string
	interfaceIndex uint32
}

func darwinCachedInterfaceLinkSpeed(ctx context.Context, interfaceName string, linkSpeeds map[string]string) string {
	if interfaceName == "" {
		return ""
	}
	if linkSpeed, ok := linkSpeeds[interfaceName]; ok {
		return linkSpeed
	}
	linkSpeed := getInterfaceLinkSpeed(ctx, interfaceName)
	linkSpeeds[interfaceName] = linkSpeed
	return linkSpeed
}

var hostnameLabelRegexp = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// isValidHostnameLabel reports whether s is a valid RFC1123 hostname label.
func isValidHostnameLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	return hostnameLabelRegexp.MatchString(s)
}
