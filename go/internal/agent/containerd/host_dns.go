package containerd

import (
	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/hostdns"
)

// warnIfHostHasNoDNS logs when a container is about to start against a host
// resolv.conf that names no resolver.
//
// This is the most expensive failure this agent can hand a developer, because
// nothing about it looks like DNS:
//
//   - the container is bind-mounted a copy on a READ-ONLY tmpfs, so it keeps the
//     empty file for its whole life even after the host gets DNS minutes later;
//   - the host itself resolves fine, so checking the host "proves" the network
//     is healthy;
//   - raw-IP egress works, so the device is plainly online;
//   - and the CLI reports "Could not connect to device. Is it powered on?",
//     which sends people looking for an outage.
//
// Observed on two separate hosts, each costing hours: a dev container that could
// not reach github.com while its host could, and another where someone had
// papered over it with hardcoded IPs in /etc/hosts -- which is why one hostname
// resolved and another did not.
//
// It is a warning rather than a repair on purpose. A container with no
// nameserver is never correct, but the agent inventing one (8.8.8.8, say) would
// be wrong on an air-gapped or split-horizon device, and would replace a
// visible fault with a silent, misrouted one. Say what is wrong and let the
// operator decide.
//
// The likely cause is startup ordering: wendyos-agent.service orders itself
// After=network.target, which means the networking stack is up -- NOT that an
// interface has an address or that DNS is configured. A boot reconcile that
// starts apps in that window hands each of them an empty resolver.
func (c *Client) warnIfHostHasNoDNS(appID string) {
	if c.logger == nil {
		return
	}
	if hostdns.Configured() {
		return
	}
	c.logger.Warn("container will start with no host DNS configuration; hostname lookups inside it will fail while raw IPs still work",
		zap.String("app_id", appID),
		zap.String("resolv_conf", hostdns.ResolvConf),
		zap.String("remedy", "give the host a working resolver, then restart this app -- the container keeps a read-only copy and will not pick up a later fix on its own"))
}
