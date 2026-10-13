package hostnetwork

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
)

// BridgeInternetChain contains CNI-owned grants for explicit Internet-enabled
// bridge apps. Local mesh policy calls it before its forwarding fallback.
const BridgeInternetChain = "WENDY-BRIDGE-INTERNET"

func InitBridgeInternetChain() error {
	if out, err := lanIPTables("-t", "filter", "-N", BridgeInternetChain); err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("create bridge Internet chain: %w (%s)", err, out)
	}
	return nil
}
func bridgeInternetMarker(owner string) string {
	return fmt.Sprintf("wendy-bridge-internet:%x", sha256.Sum256([]byte(owner)))
}
func bridgeInternetRules(owner, bridge string, ips []net.IP) ([][]string, error) {
	if owner == "" || !lanInterfaceName.MatchString(bridge) {
		return nil, fmt.Errorf("invalid bridge Internet grant")
	}
	var rules [][]string
	marker := bridgeInternetMarker(owner)
	for _, raw := range ips {
		ip := raw.To4()
		if ip == nil {
			continue
		}
		if !ip.IsGlobalUnicast() {
			return nil, fmt.Errorf("invalid bridge Internet address")
		}
		source := ip.String() + "/32"
		for _, dst := range []string{"10.88.0.0/16", "10.99.0.0/16", "0.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
			rules = append(rules, []string{"-i", bridge, "-o", "wlmi+", "-s", source, "-d", dst, "-m", "comment", "--comment", marker, "-j", "RETURN"})
		}
		rules = append(rules, []string{"-i", bridge, "-o", "wlmi+", "-s", source, "-m", "comment", "--comment", marker, "-j", "ACCEPT"})
		rules = append(rules, []string{"-i", "wlmi+", "-o", bridge, "-d", source, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-m", "comment", "--comment", marker, "-j", "ACCEPT"})

		// Native Internet uses the route selected now, rather than pinning the
		// uplink at CNI ADD and breaking Wi-Fi/Ethernet failover. This is still
		// an exact app grant: private LAN/service destinations need their own
		// WENDY-LAN projection, and raw peer/other-app links are never Internet.
		// Keep these exclusions on replies too, so an existing private-LAN
		// conntrack entry cannot bypass a withdrawn service projection.
		outbound := []string{"-i", bridge, "-s", source}
		reply := []string{"-o", bridge, "-d", source}
		for _, direction := range []struct {
			base           []string
			address, iface string
		}{{outbound, "-d", "-o"}, {reply, "-s", "-i"}} {
			deny := func(args ...string) {
				rule := append(append([]string(nil), direction.base...), args...)
				rules = append(rules, append(rule, "-m", "comment", "--comment", marker, "-j", "RETURN"))
			}
			for _, prefix := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4", "240.0.0.0/4"} {
				deny(direction.address, prefix)
			}
			for _, iface := range []string{"lo", "wlmi+", "wlmp+", "wlmesh+", "wendy+"} {
				deny(direction.iface, iface)
			}
		}
		rules = append(rules, append(outbound, "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED", "--ctdir", "ORIGINAL", "-m", "comment", "--comment", marker, "-j", "ACCEPT"))
		rules = append(rules, append(reply, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "--ctdir", "REPLY", "-m", "comment", "--comment", marker, "-j", "ACCEPT"))
	}
	return rules, nil
}

// RevokeBridgeInternetAccess works without a surviving namespace or address.
// Delete only this CNI invocation's exact marker; unrelated grants stay intact.
func RevokeBridgeInternetAccess(owner string) error {
	out, err := meshIPTables("-t", "filter", "-S", BridgeInternetChain)
	if err != nil {
		if exitCode(err) == 1 {
			return revokeBridgeReplyRules(owner)
		}
		return fmt.Errorf("inspect bridge Internet grants: %w: %s", err, out)
	}
	marker := bridgeInternetMarker(owner)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "-A" || fields[1] != BridgeInternetChain {
			continue
		}
		for i := range fields {
			fields[i] = strings.Trim(fields[i], "\"")
		}
		owned := false
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "--comment" && fields[i+1] == marker {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		// Match the full rule instead of a line number: another CNI process may
		// delete an earlier sibling rule between this observation and deletion.
		if output, e := meshIPTables(append([]string{"-t", "filter", "-D", BridgeInternetChain}, fields[2:]...)...); e != nil && exitCode(e) != 1 {
			return fmt.Errorf("revoke bridge Internet grant: %w: %s", e, output)
		}
	}
	return revokeBridgeReplyRules(owner)
}
func EnsureBridgeInternetAccess(owner, bridge string, ips []net.IP) (err error) {
	rules, err := bridgeInternetRules(owner, bridge, ips)
	if err != nil {
		return err
	}
	if err = InitBridgeInternetChain(); err != nil {
		return err
	}
	if err = RevokeBridgeInternetAccess(owner); err != nil {
		return err
	}
	// Revoke removed any previously recorded ownership. Refuse to adopt an
	// otherwise matching unrecorded rule in the reserved reply-rule slot.
	for _, raw := range ips {
		if ip := raw.To4(); ip != nil {
			present, e := bridgeReplyRulePresent(ip.String())
			if e != nil {
				return e
			}
			if present {
				return fmt.Errorf("unowned bridge reply rule already exists")
			}
		}
	}
	if err = rememberBridgeGrant(owner, bridge, ips); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = RevokeBridgeInternetAccess(owner)
		}
	}()
	for _, rule := range rules {
		if out, e := meshIPTables(append([]string{"-t", "filter", "-A", BridgeInternetChain}, rule...)...); e != nil {
			return fmt.Errorf("grant bridge Internet access: %w: %s", e, out)
		}
	}
	for _, raw := range ips {
		if ip := raw.To4(); ip != nil {
			if err = ensureBridgeReplyRule(ip.String()); err != nil {
				return err
			}
		}
	}
	return nil
}
func CheckBridgeInternetAccess(owner, bridge string, ips []net.IP) error {
	rules, err := bridgeInternetRules(owner, bridge, ips)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if out, e := meshIPTables(append([]string{"-t", "filter", "-C", BridgeInternetChain}, rule...)...); e != nil {
			return fmt.Errorf("bridge Internet grant missing: %w: %s", e, out)
		}
	}
	for _, raw := range ips {
		if ip := raw.To4(); ip != nil {
			ok, err := bridgeReplyRulePresent(ip.String())
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("bridge reply rule missing")
			}
		}
	}
	return nil
}
