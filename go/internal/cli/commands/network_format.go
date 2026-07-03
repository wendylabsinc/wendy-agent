package commands

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// formatNetworkInterfaces renders the device's network interfaces and their IP
// addresses as an aligned block suitable for printing under `wendy device info`.
// The returned string ends with a trailing newline. Interfaces with no
// addresses are skipped.
func formatNetworkInterfaces(ifaces []*agentpb.NetworkInterface) string {
	var b strings.Builder
	b.WriteString("Network:\n")

	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, iface := range ifaces {
		if len(iface.GetIpAddresses()) == 0 {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\n", iface.GetName(), strings.Join(iface.GetIpAddresses(), ", "))
	}
	tw.Flush()

	return b.String()
}

// bestReachableIP picks the IP address most likely to be reachable from a
// developer's machine (e.g. to open an app in a browser). IPv4 addresses are
// preferred over IPv6, and interfaces are considered in the order the agent
// reported them. Returns "" when no address is available.
func bestReachableIP(ifaces []*agentpb.NetworkInterface) string {
	var firstAny string
	for _, iface := range ifaces {
		for _, addr := range iface.GetIpAddresses() {
			ip := net.ParseIP(addr)
			if ip == nil {
				continue
			}
			if ip.To4() != nil {
				return addr
			}
			if firstAny == "" {
				firstAny = addr
			}
		}
	}
	return firstAny
}

// reachableAppURL builds a browser-openable URL for a freshly started app,
// using a routable device IP instead of the (often unresolvable) .local
// hostname that `wendy run` otherwise reports.
//
// When the app configures a postStart openURL that references the device
// hostname, that template is reused verbatim (scheme, port, and path) with the
// IP swapped in, so the printed URL matches what the browser hook opens.
// Otherwise, if readiness defines a TCP port, an http URL on that port is
// assumed. Returns "" when no reachable URL can be derived.
func reachableAppURL(hookURL, appID, deviceIP string, readiness *appconfig.ReadinessConfig) string {
	if deviceIP == "" {
		return ""
	}
	if hookURL != "" && strings.Contains(hookURL, "WENDY_HOSTNAME") {
		return expandHookEnv(hookURL, deviceIP, appID)
	}
	if readiness != nil && readiness.TCPSocket != nil && readiness.TCPSocket.Port != 0 {
		return "http://" + net.JoinHostPort(deviceIP, strconv.Itoa(readiness.TCPSocket.Port))
	}
	return ""
}
