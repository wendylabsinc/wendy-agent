// Package hostdns answers one question: does this host currently have a
// resolver?
//
// It exists because the answer decides whether starting a container now
// permanently breaks its DNS. A container that gets no written resolv.conf
// inherits the host's on a read-only tmpfs and keeps that copy for its whole
// life -- so an app started in the window between the network stack coming up
// and DHCP completing can never resolve a hostname, and will not pick up a
// later fix.
//
// Its own package because both the container start path and the boot reconcile
// need it, and those live in packages that do not import each other.
package hostdns

import (
	"context"
	"os"
	"strings"
	"time"
)

// ResolvConf is the file a container without gateway DNS ends up seeing:
// containerd propagates the host's copy.
const ResolvConf = "/etc/resolv.conf"

// HasNameserver reports whether a resolv.conf names any resolver.
//
// Content, not existence. Every file that causes this problem is readable and
// populated: systemd-resolved writes "# No DNS servers known." with no
// nameserver line before it has learned an upstream, and a NetworkManager host
// has a search-only file until DHCP completes.
func HasNameserver(data string) bool {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		field, rest, ok := strings.Cut(line, " ")
		if !ok {
			field, rest, ok = strings.Cut(line, "\t")
		}
		if !ok || !strings.EqualFold(strings.TrimSpace(field), "nameserver") {
			continue
		}
		if strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}

// readFile is the seam tests replace, so the wait can be exercised without a
// real resolv.conf.
var readFile = os.ReadFile

// ConfiguredAt reports whether the resolv.conf at path names a resolver.
// Unreadable counts as unconfigured: absent is as broken as empty.
func ConfiguredAt(path string) bool {
	data, err := readFile(path)
	if err != nil {
		return false
	}
	return HasNameserver(string(data))
}

// Configured reports whether the host has a resolver right now.
func Configured() bool { return ConfiguredAt(ResolvConf) }

// WaitTimeout bounds how long a caller will hold off on work that a missing
// resolver would silently break.
//
// Generous enough for DHCP on a slow link, short enough that a device with no
// network at all is not held up for long -- and it must always be bounded:
// plenty of devices are deliberately offline, and their apps still have to
// start.
const WaitTimeout = 90 * time.Second

// waitPoll is how often the file is re-read. resolv.conf is written once when
// the lease arrives, so this only decides how quickly that is noticed.
const waitPoll = 500 * time.Millisecond

// WaitConfigured blocks until the host has a resolver, the timeout expires, or
// ctx is cancelled. It reports whether a resolver was found.
//
// Returning false is not an error: it means "proceed anyway". An offline device
// must still start its apps, and this is a best-effort delay rather than a
// precondition.
func WaitConfigured(ctx context.Context, timeout time.Duration, onWait func(), onTimeout func()) bool {
	if Configured() {
		return true
	}
	if onWait != nil {
		onWait()
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(waitPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			if Configured() {
				return true
			}
			if !time.Now().Before(deadline) {
				if onTimeout != nil {
					onTimeout()
				}
				return false
			}
		}
	}
}
