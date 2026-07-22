package discovery

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// USBNetworkInterfaceNames returns the names of non-loopback network interfaces
// that appear to be USB-attached (USB-CDC gadget links), using the same
// heuristics as device discovery (name patterns plus, on Linux, a sysfs
// device-path check). The CLI's USB-C auto-setup flow uses it to locate the
// host's gadget interface.
func USBNetworkInterfaceNames() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}
	var out []string
	for i := range ifaces {
		if ifaces[i].Flags&net.FlagLoopback != 0 {
			continue
		}
		if looksLikeUSBConnection(ifaces[i].Name, "") {
			out = append(out, ifaces[i].Name)
		}
	}
	return out, nil
}

// ansiEscapeRE matches ANSI/VT100 escape sequences (e.g. colour codes).
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// SanitiseDisplayName sanitises a device name or version string sourced from
// the network (mDNS, TXT records) before it is rendered in the terminal.
func SanitiseDisplayName(s string) string { return sanitiseNetworkString(s, 64) }

// sanitiseNetworkString strips ANSI escape sequences and C0/C1/DEL control
// characters from a string sourced from the network, then truncates to maxLen
// runes. This prevents terminal injection from rogue mDNS/DNS-SD advertisers.
func sanitiseNetworkString(s string, maxLen int) string {
	s = ansiEscapeRE.ReplaceAllString(s, "")
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	s = strings.TrimSpace(b.String())
	if maxLen > 0 {
		runes := []rune(s)
		if len(runes) > maxLen {
			s = string(runes[:maxLen])
		}
	}
	return s
}

var linuxUSBInterfaceNameRE = regexp.MustCompile(`^en[a-z0-9]*u[0-9]+`)

func setLANNetworkInterface(dev *models.LANDevice, interfaceName, displayName, linkSpeed string) {
	interfaceName = strings.TrimSpace(sanitiseNetworkString(interfaceName, 64))
	if interfaceName == "" {
		return
	}

	dev.NetworkInterface = interfaceName
	if dev.USB == "" {
		dev.USB = usbConnectionSummary(interfaceName, sanitiseNetworkString(displayName, 64), sanitiseNetworkString(linkSpeed, 32))
	}
}

func usbConnectionSummary(interfaceName, displayName, linkSpeed string) string {
	if !looksLikeUSBConnection(interfaceName, displayName) {
		return ""
	}

	label := interfaceName
	if displayName != "" && !strings.EqualFold(displayName, interfaceName) {
		label = fmt.Sprintf("%s (%s)", displayName, interfaceName)
	}
	if linkSpeed != "" {
		return label + " " + linkSpeed
	}
	return label
}

func looksLikeUSBConnection(interfaceName, displayName string) bool {
	name := strings.ToLower(strings.TrimSpace(interfaceName))
	display := strings.ToLower(strings.TrimSpace(displayName))
	combined := name + " " + display

	switch {
	case strings.Contains(combined, "wendy"):
		return true
	case strings.Contains(combined, "usb"):
		return true
	case strings.Contains(combined, "rndis"):
		return true
	case strings.Contains(combined, "ndis"):
		return true
	case strings.Contains(combined, "ecm"):
		return true
	case strings.Contains(combined, "gadget"):
		return true
	case strings.HasPrefix(name, "enx"):
		return true
	case linuxUSBInterfaceNameRE.MatchString(name):
		return true
	default:
		// Fall back to sysfs: an interface whose real device path traverses the
		// USB bus is USB-backed regardless of its name. This keeps gadget
		// detection working under net.ifnames=0 / classic naming (eth0, usb0),
		// where none of the name heuristics above match. No-op off Linux.
		return interfaceIsUSBBacked(strings.TrimSpace(interfaceName))
	}
}

func appendPreferredLANDevice(devices []models.LANDevice, indexes map[string]int, key string, dev models.LANDevice) []models.LANDevice {
	if idx, ok := indexes[key]; ok {
		if preferDiscoveredLANDevice(dev, devices[idx]) {
			devices[idx] = dev
		}
		return devices
	}

	indexes[key] = len(devices)
	return append(devices, dev)
}

func preferDiscoveredLANDevice(candidate, existing models.LANDevice) bool {
	if (candidate.USB != "") != (existing.USB != "") {
		return candidate.USB != ""
	}
	if existing.IPAddress == "" && candidate.IPAddress != "" {
		return true
	}
	// Same device advertised at both an IPv4 and an IPv6 address (avahi emits
	// one resolve entry per protocol): keep the IPv4 one. A device's IPv6 set
	// typically leads with an RFC 4941 temporary (privacy) address that
	// rotates away, so dialing or probing the stored IPv6 later goes stale.
	if candidate.IPAddress != "" && existing.IPAddress != "" {
		if c4, e4 := isIPv4LANAddress(candidate.IPAddress), isIPv4LANAddress(existing.IPAddress); c4 != e4 {
			return c4
		}
	}
	if existing.NetworkInterface == "" && candidate.NetworkInterface != "" {
		return true
	}
	return lanDeviceDiscoveryScore(candidate) > lanDeviceDiscoveryScore(existing)
}

func lanDeviceDiscoveryScore(dev models.LANDevice) int {
	score := 0
	if dev.ID != "" {
		score++
	}
	if dev.DisplayName != "" {
		score++
	}
	if dev.Hostname != "" {
		score++
	}
	if dev.IPAddress != "" {
		score++
	}
	if isRoutableLANAddress(dev.IPAddress) {
		score++
	}
	if dev.Port != 0 {
		score++
	}
	if dev.NetworkInterface != "" {
		score++
	}
	if dev.USB != "" {
		score += 2
	}
	if dev.IsMTLS {
		score++
	}
	return score
}

// isIPv4LANAddress reports whether addr (optionally "%zone"-suffixed) is an
// IPv4 or IPv4-mapped address.
func isIPv4LANAddress(addr string) bool {
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	a, err := netip.ParseAddr(addr)
	return err == nil && (a.Is4() || a.Is4In6())
}

// isRoutableLANAddress reports whether addr is a directly dialable address —
// all IPv4 (including 169.254.0.0/16 link-local) or non-link-local IPv6 —
// as opposed to an IPv6 link-local unicast address (fe80::/10), which needs a
// zone id and is a poor default dial target. A "%zone" suffix is stripped
// before parsing; an empty or unparseable address is treated as non-routable.
func isRoutableLANAddress(addr string) bool {
	if i := strings.IndexByte(addr, '%'); i >= 0 {
		addr = addr[:i]
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false
	}
	if a.Is4() || a.Is4In6() {
		return true
	}
	return !a.IsLinkLocalUnicast()
}
