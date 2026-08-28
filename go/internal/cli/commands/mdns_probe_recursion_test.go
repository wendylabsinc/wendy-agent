package commands

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// countingBrowse replaces lanBrowseFn for the test and reports how many times a
// browse was started.
func countingBrowse(t *testing.T) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	orig := lanBrowseFn
	t.Cleanup(func() { lanBrowseFn = orig })
	lanBrowseFn = func(context.Context, time.Duration) ([]models.LANDevice, error) {
		calls.Add(1)
		return []models.LANDevice{{Hostname: "orin.local", IPAddress: "192.168.0.9"}}, nil
	}
	return &calls
}

// The cycle this guards: a discovery probe dials a device, the dial cannot
// resolve its ".local" name, and the mDNS fallback starts a WHOLE NEW discovery
// session — which probes, which dials, which browses. Branching factor
// probeWorkers per level, bounded only by each level's timeout. One
// `wendy device logs` reached 934 MB of live heap this way.
func TestResolveMDNSHostRefusesToBrowseInsideAProbe(t *testing.T) {
	calls := countingBrowse(t)

	got := resolveMDNSHost(discovery.WithinProbe(context.Background()), "orin.local")

	if got != "" {
		t.Fatalf("resolveMDNSHost returned %q inside a probe; it must decline", got)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("started %d LAN browse(s) from inside a probe; the recursion is not broken", n)
	}
}

// Outside a probe the fallback must still work — it is what keeps ".local"
// names dialable on platforms whose resolver does not do mDNS (issue #1155).
func TestResolveMDNSHostStillBrowsesOutsideAProbe(t *testing.T) {
	calls := countingBrowse(t)

	got := resolveMDNSHost(context.Background(), "orin.local")

	if got != "192.168.0.9" {
		t.Fatalf("resolveMDNSHost = %q, want the advertised IP 192.168.0.9", got)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("browsed %d times, want exactly 1", n)
	}
}

// resolveHostMDNSFallback is the frame the dial path actually calls, so the
// guard has to hold through it too — a literal IP short-circuits before any
// browse, and an unresolvable name inside a probe must not fall through to one.
func TestResolveHostMDNSFallbackDoesNotBrowseInsideAProbe(t *testing.T) {
	calls := countingBrowse(t)
	origLookup := osLookupHostFn
	t.Cleanup(func() { osLookupHostFn = origLookup })
	osLookupHostFn = func(context.Context, string) ([]string, error) {
		return nil, context.DeadlineExceeded // force the mDNS fallback
	}

	got := resolveHostMDNSFallback(discovery.WithinProbe(context.Background()), "orin.local")

	if got != "" {
		t.Fatalf("resolveHostMDNSFallback returned %q inside a probe, want \"\"", got)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("started %d LAN browse(s) from inside a probe; the recursion is not broken", n)
	}
}
