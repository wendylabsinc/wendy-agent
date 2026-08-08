//go:build linux || windows

package discovery

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// ── hashicorpEntryToService (pure logic, no network) ──────────────────────

func TestHashicorpEntryToServiceFiltersWrongServiceType(t *testing.T) {
	entry := &mdns.ServiceEntry{
		Name: "phone._remotepairing._tcp.local.",
		Host: "phone.local.",
		Port: 1234,
	}

	if _, ok := hashicorpEntryToService(entry, nil, wendyServiceType); ok {
		t.Fatal("hashicorpEntryToService returned true for a non-matching service type")
	}
}

func TestHashicorpEntryToServicePrefersIPv4AndParsesTXT(t *testing.T) {
	iface := &net.Interface{Name: "eth0"}
	entry := &mdns.ServiceEntry{
		Name:       "wendyos-prudent-lark._wendyos._udp.local.",
		Host:       "wendyos-prudent-lark.local.",
		AddrV4:     net.ParseIP("192.168.1.42"),
		AddrV6:     net.ParseIP("fe80::1"),
		Port:       50051,
		InfoFields: []string{"id=agent-id", "displayname=Prudent Lark", "tls=true"},
	}

	svc, ok := hashicorpEntryToService(entry, iface, wendyServiceType)
	if !ok {
		t.Fatal("hashicorpEntryToService returned false for a matching entry")
	}
	if svc.InstanceName != "wendyos-prudent-lark" {
		t.Errorf("InstanceName = %q, want %q", svc.InstanceName, "wendyos-prudent-lark")
	}
	if svc.Hostname != "wendyos-prudent-lark.local" {
		t.Errorf("Hostname = %q, want %q", svc.Hostname, "wendyos-prudent-lark.local")
	}
	if svc.IPAddress != "192.168.1.42" {
		t.Errorf("IPAddress = %q, want the IPv4 address preferred over IPv6", svc.IPAddress)
	}
	if svc.Port != 50051 {
		t.Errorf("Port = %d, want 50051", svc.Port)
	}
	if svc.TXTRecords["displayname"] != "Prudent Lark" || svc.TXTRecords["tls"] != "true" {
		t.Errorf("TXTRecords = %v, want displayname/tls parsed", svc.TXTRecords)
	}
	if svc.InterfaceName != "eth0" {
		t.Errorf("InterfaceName = %q, want %q", svc.InterfaceName, "eth0")
	}
}

func TestHashicorpEntryToServiceIPv6LinkLocalGetsZoneSuffix(t *testing.T) {
	iface := &net.Interface{Name: "eth0"}
	entry := &mdns.ServiceEntry{
		Name:   "wendyos-foo._wendyos._udp.local.",
		Host:   "wendyos-foo.local.",
		AddrV6: net.ParseIP("fe80::1"),
		Port:   50051,
	}

	svc, ok := hashicorpEntryToService(entry, iface, wendyServiceType)
	if !ok {
		t.Fatal("hashicorpEntryToService returned false for a matching entry")
	}
	if svc.IPAddress != "fe80::1%eth0" {
		t.Errorf("IPAddress = %q, want zone-scoped fe80::1%%eth0", svc.IPAddress)
	}
}

func TestHashicorpEntryToServiceNilInterfaceOmitsZoneAndName(t *testing.T) {
	entry := &mdns.ServiceEntry{
		Name:   "wendyos-foo._wendyos._udp.local.",
		Host:   "wendyos-foo.local.",
		AddrV6: net.ParseIP("fe80::1"),
		Port:   50051,
	}

	// nil is the sweep target Windows uses for its default/all-interface
	// query, where hashicorp/mdns cannot say which adapter answered.
	svc, ok := hashicorpEntryToService(entry, nil, wendyServiceType)
	if !ok {
		t.Fatal("hashicorpEntryToService returned false for a matching entry")
	}
	if svc.InterfaceName != "" {
		t.Errorf("InterfaceName = %q, want empty for a nil interface", svc.InterfaceName)
	}
	if svc.IPAddress != "fe80::1" {
		t.Errorf("IPAddress = %q, want unscoped (no interface to scope it to)", svc.IPAddress)
	}
}

// ── hashicorpSweepTargets (pure logic, injected interface list) ───────────

func TestHashicorpSweepTargetsIncludesNilOnWindowsOnly(t *testing.T) {
	origIfaces := ifaceListFn
	t.Cleanup(func() { ifaceListFn = origIfaces })

	fake := []net.Interface{{Name: "eth-fake-0"}}
	ifaceListFn = func() []net.Interface { return fake }

	targets := hashicorpSweepTargets()

	wantLen := len(fake)
	wantNil := runtime.GOOS == "windows"
	if wantNil {
		wantLen++
	}
	if len(targets) != wantLen {
		t.Fatalf("hashicorpSweepTargets() returned %d targets, want %d", len(targets), wantLen)
	}

	hasNil := false
	for _, tgt := range targets {
		if tgt == nil {
			hasNil = true
		}
	}
	if hasNil != wantNil {
		t.Errorf("nil (default-interface) target present = %v, want %v (runtime.GOOS=%s)", hasNil, wantNil, runtime.GOOS)
	}
}

// ── parallel sweep timing (pure logic, injected interfaces + query fn) ────

// TestHashicorpSweepOnceQueriesInterfacesInParallel is the property the
// streaming engine depends on for "instant" discovery: a sweep across
// several interfaces must not serialize them, or the effective latency for a
// device on the Nth interface would scale with N. sweepQueryFn is faked to
// simulate a slow query without touching the network, so the test is
// deterministic and requires no multicast privileges.
func TestHashicorpSweepOnceQueriesInterfacesInParallel(t *testing.T) {
	origIfaces := ifaceListFn
	origQuery := sweepQueryFn
	t.Cleanup(func() {
		ifaceListFn = origIfaces
		sweepQueryFn = origQuery
	})

	fakeIfaces := []net.Interface{{Name: "eth-fake-0"}, {Name: "eth-fake-1"}, {Name: "eth-fake-2"}}
	ifaceListFn = func() []net.Interface { return fakeIfaces }

	const perQueryDelay = 100 * time.Millisecond

	var mu sync.Mutex
	var calls []string
	sweepQueryFn = func(ctx context.Context, iface *net.Interface, _ string, _ chan *mdns.ServiceEntry, _ time.Duration) error {
		name := "nil"
		if iface != nil {
			name = iface.Name
		}
		mu.Lock()
		calls = append(calls, name)
		mu.Unlock()

		select {
		case <-time.After(perQueryDelay):
		case <-ctx.Done():
		}
		return nil
	}

	start := time.Now()
	hashicorpSweepOnce(context.Background(), wendyServiceType, func(MDNSService) {})
	elapsed := time.Since(start)

	if elapsed >= 2*perQueryDelay {
		t.Errorf("sweep across %d fake interfaces took %v, want < %v (interfaces must be queried in parallel, not serially)",
			len(fakeIfaces), elapsed, 2*perQueryDelay)
	}

	mu.Lock()
	defer mu.Unlock()
	wantCalls := len(fakeIfaces)
	if runtime.GOOS == "windows" {
		wantCalls++ // the nil default-interface target
	}
	if len(calls) != wantCalls {
		t.Errorf("sweepQueryFn called %d times, want %d (calls: %v)", len(calls), wantCalls, calls)
	}
}

// ── live fixture (advertises via hashicorp/mdns's own server) ─────────────

// TestHashicorpStreamBackendLiveAdvertise is the end-to-end property this
// backend exists for: an advertised service arrives within one sweep, and
// arrives again after a re-query — proving the backend keeps re-sweeping
// rather than querying once and going quiet. It advertises its own
// hashicorp/mdns server in-process (mirroring hashicorp/mdns's own
// TestServer_Lookup) rather than depending on a real device on the network,
// and skips outright in a sandbox that cannot open a multicast listener.
func TestHashicorpStreamBackendLiveAdvertise(t *testing.T) {
	const serviceType = "_wendy-hcbackendtest._tcp"
	instance := fmt.Sprintf("wendy-hcbackendtest-%d", os.Getpid())
	const port = 51260
	want := map[string]string{"displayname": "HC Backend Test", "tls": "true"}

	svc, err := mdns.NewMDNSService(instance, serviceType, "", "", port, nil, txtSliceFrom(want))
	if err != nil {
		t.Fatalf("mdns.NewMDNSService: %v", err)
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: svc})
	if err != nil {
		t.Skipf("cannot start an mDNS server (no multicast listener available in this environment): %v", err)
	}
	t.Cleanup(func() { _ = server.Shutdown() })

	origRequery, origTimeout := hashicorpRequeryDelay, hashicorpSweepTimeout
	hashicorpRequeryDelay = 200 * time.Millisecond
	hashicorpSweepTimeout = 2 * time.Second
	t.Cleanup(func() {
		hashicorpRequeryDelay = origRequery
		hashicorpSweepTimeout = origTimeout
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	type arrival struct {
		svc MDNSService
		at  time.Time
	}
	arrivals := make(chan arrival, 64)
	done := make(chan error, 1)
	go func() {
		done <- hashicorpStreamBackend(ctx, serviceType, func(got MDNSService) {
			if got.InstanceName != instance {
				return
			}
			arrivals <- arrival{svc: got, at: time.Now()}
		})
	}()

	waitFor := hashicorpSweepTimeout + 3*time.Second

	var first, second arrival
	select {
	case first = <-arrivals:
	case <-time.After(waitFor):
		t.Fatal("advertised service did not arrive within the first sweep")
	}
	select {
	case second = <-arrivals:
	case <-time.After(waitFor):
		t.Fatal("advertised service did not arrive again after a re-query")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("hashicorpStreamBackend returned %v, want nil after ctx cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hashicorpStreamBackend did not return after ctx cancellation (a goroutine outlived the call)")
	}

	if !second.at.After(first.at) {
		t.Error("second arrival was not after the first (re-query did not happen)")
	}
	for _, a := range []arrival{first, second} {
		if a.svc.Port != port {
			t.Errorf("Port = %d, want %d", a.svc.Port, port)
		}
		for k, v := range want {
			if a.svc.TXTRecords[k] != v {
				t.Errorf("TXT[%q] = %q, want %q", k, a.svc.TXTRecords[k], v)
			}
		}
	}
}

// txtSliceFrom encodes a TXT record map into hashicorp/mdns's
// []string{"key=value", ...} form for MDNSService.
func txtSliceFrom(records map[string]string) []string {
	out := make([]string, 0, len(records))
	for k, v := range records {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}
