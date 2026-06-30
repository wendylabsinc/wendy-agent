package commands

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// hostNetworkDiagnostics runs macOS host-side network diagnostics to explain
// why a device is unreachable. It is the in-product version of the
// wendy-connectivity-debug skill: it inspects this machine's default route,
// looks for a VPN diverting traffic, compares subnets, and pings the agent.
//
// agentIP is the resolved agent IP (may be a hostname if resolution failed);
// ports are the agent ports already probed by the caller. Results are returned
// as checkResults so they fold into the doctor report.
func hostNetworkDiagnostics(agentIP string, ports []int) []checkResult {
	var out []checkResult

	iface, gateway := defaultRoute()
	myIP := ""
	if iface != "" {
		myIP = interfaceIPv4(iface)
	}

	// This machine's network.
	if iface == "" || myIP == "" {
		out = append(out, checkResult{
			Name:   "Local network",
			Status: statusWarn,
			Detail: "could not determine this machine's default interface/IP",
		})
	} else {
		out = append(out, checkResult{
			Name:   "Local network",
			Status: statusPass,
			Detail: fmt.Sprintf("%s on %s (gateway %s)", myIP, iface, gateway),
		})
	}

	// Subnet comparison (only meaningful when both IPs are real IPv4; myIP
	// always is, agentIP may be IPv6 — subnet24 is IPv4-only, so guard on To4).
	if myIP != "" && net.ParseIP(agentIP).To4() != nil {
		if subnet24(myIP) != subnet24(agentIP) {
			out = append(out, checkResult{
				Name:   "Subnet",
				Status: statusFail,
				Detail: fmt.Sprintf("this machine is on %s.x but the agent is on %s.x", subnet24(myIP), subnet24(agentIP)),
				Hint:   "Different subnets — join the device's network, or use Wendy Cloud / USB instead.",
			})
		} else {
			out = append(out, checkResult{Name: "Subnet", Status: statusPass, Detail: "same /24 as the agent"})
		}
	}

	// VPN / tunnel. Every Mac runs utun* interfaces for system services
	// (iCloud Private Relay, Handoff, Personal Hotspot), so their mere presence
	// is not a VPN signal. Only flag a problem when the route to *this agent*
	// is actually diverted through a tunnel.
	if net.ParseIP(agentIP) != nil {
		if routeIface := routeInterface(agentIP); strings.HasPrefix(routeIface, "utun") {
			out = append(out, checkResult{
				Name:   "VPN / tunnel",
				Status: statusFail,
				Detail: fmt.Sprintf("route to %s uses %s, not your LAN interface", agentIP, routeIface),
				Hint:   "A VPN is diverting the agent route — disconnect it or split-tunnel the device's subnet.",
			})
		} else {
			out = append(out, checkResult{Name: "VPN / tunnel", Status: statusPass, Detail: "agent route does not go through a VPN tunnel"})
		}
	}

	// Reachability via ping + MAC.
	if net.ParseIP(agentIP) != nil {
		if pingHost(agentIP) {
			detail := agentIP + " responds to ping"
			if mac := arpMAC(agentIP); mac != "" {
				detail += fmt.Sprintf(" (MAC %s)", mac)
			}
			out = append(out, checkResult{
				Name:   "Host reachability",
				Status: statusWarn,
				Detail: detail,
				Hint:   "Host is up but the agent port is closed — the agent may be down or a stale IP. Re-check with 'wendy device info'.",
			})
		} else {
			out = append(out, checkResult{
				Name:   "Host reachability",
				Status: statusFail,
				Detail: agentIP + " does not respond to ping",
				Hint:   "Host appears offline or unreachable — verify it's powered on and on this network.",
			})
		}
	}

	return out
}

func defaultRoute() (iface, gateway string) {
	out, err := runCmd(1*time.Second, "route", "-n", "get", "default")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "interface:":
			iface = fields[1]
		case "gateway:":
			gateway = fields[1]
		}
	}
	return iface, gateway
}

func interfaceIPv4(iface string) string {
	out, err := runCmd(1*time.Second, "ipconfig", "getifaddr", iface)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func routeInterface(ip string) string {
	out, err := runCmd(1*time.Second, "route", "-n", "get", ip)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "interface:" {
			return fields[1]
		}
	}
	return ""
}

func pingHost(ip string) bool {
	_, err := runCmd(2*time.Second, "ping", "-c1", "-t1", ip)
	return err == nil
}

var macRe = regexp.MustCompile(`([0-9a-fA-F]{1,2}(:[0-9a-fA-F]{1,2}){5})`)

func arpMAC(ip string) string {
	out, err := runCmd(1*time.Second, "arp", "-n", ip)
	if err != nil {
		return ""
	}
	if m := macRe.FindString(out); m != "" {
		return m
	}
	return ""
}

// subnet24 returns the first three octets of an IPv4 address (e.g. "192.168.0").
func subnet24(ip string) string {
	if idx := strings.LastIndex(ip, "."); idx > 0 {
		return ip[:idx]
	}
	return ip
}

func runCmd(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}
