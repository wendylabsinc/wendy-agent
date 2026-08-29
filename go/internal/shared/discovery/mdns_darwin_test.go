//go:build darwin

package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestBrowseMDNSServicesContinuousStreamsLateArrival is the property the picker
// depends on: a service that appears *after* the browse is already open still
// reaches the consumer, without waiting for a rescan. Registering only once the
// stream is running is what separates streaming from polling — a polling
// implementation would pass a test that registered up front.
func TestBrowseMDNSServicesContinuousStreamsLateArrival(t *testing.T) {
	// Its own service type so a browse cannot pick up real devices, and a pid in
	// the instance name so concurrent test binaries do not collide and trigger
	// mDNSResponder's conflict renaming.
	const serviceType = "_wendy-streamtest._tcp"
	instance := fmt.Sprintf("wendy-streamtest-%d", os.Getpid())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ch, err := BrowseMDNSServicesContinuous(ctx, serviceType)
	if err != nil {
		t.Fatalf("BrowseMDNSServicesContinuous: %v", err)
	}

	want := map[string]string{"name": "Stream Test Board", "mtls": "true"}
	stop, err := dnssdRegister(instance, serviceType, 51235, want)
	if err != nil {
		t.Skipf("cannot register an mDNS service (is mDNSResponder reachable?): %v", err)
	}
	t.Cleanup(stop)

	var got MDNSService
	deadline := time.After(20 * time.Second)
	for got.InstanceName != instance {
		select {
		case svc, ok := <-ch:
			if !ok {
				t.Fatal("stream closed before the registered service arrived")
			}
			got = svc
		case <-deadline:
			t.Fatal("registered service did not arrive on the stream")
		}
	}

	if got.Port != 51235 {
		t.Errorf("Port = %d, want 51235", got.Port)
	}
	if got.Hostname == "" {
		t.Error("resolved service has an empty hostname")
	}
	// A TXT value with a space, which is what the old display-output parser had
	// to recover by unescaping.
	for k, v := range want {
		if got.TXTRecords[k] != v {
			t.Errorf("TXT[%q] = %q, want %q", k, got.TXTRecords[k], v)
		}
	}

	// BrowseMDNSServicesContinuous does not deduplicate (see
	// TestBrowseMDNSServicesContinuousForwardsUntilCancel in mdns_test.go):
	// mDNSResponder repeats an Add per interface and on ordinary answer
	// refresh, and callers that care about repeats (the device picker) merge
	// by identity themselves. This drains whatever else arrives without
	// asserting on it, then checks cancellation still closes the channel
	// promptly.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range ch {
		}
	}()

	// Cancelling must close the channel — the picker's signal to fall back to
	// polling, and what stops this goroutine.
	cancel()
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not close after ctx cancellation")
	}
}

// TestMDNSStreamBackend is the property the streaming LAN engine (stream.go's
// lanBackendFn seam) depends on: every browse "Add" gets resolved and emitted
// promptly, in parallel, rather than one at a time inside the browse callback
// (the bug this backend exists to fix — see mdnsStreamBackend's doc comment).
// Three instances are registered concurrently; if resolves were serialized in
// the callback, the third would only arrive after two resolve timeouts' worth
// of head-of-line blocking, which the arrival-time assertion below would
// catch.
func TestMDNSStreamBackend(t *testing.T) {
	const serviceType = "_wendy-backendtest._tcp"
	pid := os.Getpid()
	want := map[string]string{"displayname": "Backend Test", "tls": "true"}

	const n = 3
	instances := make([]string, n)
	wantPorts := make(map[string]int, n)
	for i := range instances {
		instance := fmt.Sprintf("wendy-backendtest-%d-%d", pid, i)
		port := 51240 + i
		stop, err := dnssdRegister(instance, serviceType, uint16(port), want)
		if err != nil {
			t.Skipf("cannot register an mDNS service (is mDNSResponder reachable?): %v", err)
		}
		t.Cleanup(stop)
		instances[i] = instance
		wantPorts[instance] = port
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type arrival struct {
		svc MDNSService
		at  time.Duration
	}
	emitted := make(chan arrival, 16)
	start := time.Now()

	done := make(chan error, 1)
	go func() {
		done <- mdnsStreamBackend(ctx, serviceType, func(svc MDNSService) {
			emitted <- arrival{svc: svc, at: time.Since(start)}
		})
	}()

	got := make(map[string]arrival, n)
	for len(got) < n {
		select {
		case a := <-emitted:
			got[a.svc.InstanceName] = a
		case err := <-done:
			t.Fatalf("mdnsStreamBackend returned before all %d instances arrived (got %d): %v", n, len(got), err)
		case <-time.After(4 * time.Second):
			t.Fatalf("timed out waiting for all %d instances; got %d of them", n, len(got))
		}
	}

	// Cancelling must make the backend return promptly, with its resolver
	// pool fully drained — no goroutine may outlive the call.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("mdnsStreamBackend returned %v, want nil after ctx cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mdnsStreamBackend did not return after ctx cancellation")
	}

	for _, inst := range instances {
		a, ok := got[inst]
		if !ok {
			t.Errorf("instance %q never emitted", inst)
			continue
		}
		if a.svc.Port != wantPorts[inst] {
			t.Errorf("instance %q: Port = %d, want %d", inst, a.svc.Port, wantPorts[inst])
		}
		for k, v := range want {
			if a.svc.TXTRecords[k] != v {
				t.Errorf("instance %q: TXT[%q] = %q, want %q", inst, k, a.svc.TXTRecords[k], v)
			}
		}
		if a.at >= 3*time.Second {
			t.Errorf("instance %q arrived after %v, want < 3s (not waiting for ctx end)", inst, a.at)
		}
	}
}

// TestMDNSStreamResolveAndEmitFallback pins mdnsStreamResolveAndEmit's
// isValidHostnameLabel gate on a failed resolve, in both directions: a name
// that can stand in as a hostname label still surfaces a bare identity, so a
// device with no TXT records or a transient resolve failure is not silently
// dropped from the stream — but a name that cannot (e.g. one containing a
// space) is skipped rather than emitting a misleading dialable-looking
// identity. Deterministic and network-free: resolveServiceFn is swapped for
// a resolver that always fails, so no mDNSResponder round trip is involved.
func TestMDNSStreamResolveAndEmitFallback(t *testing.T) {
	orig := resolveServiceFn
	t.Cleanup(func() { resolveServiceFn = orig })
	resolveServiceFn = func(context.Context, browseResult, string) (MDNSService, error) {
		return MDNSService{}, errors.New("forced resolve failure")
	}

	t.Run("valid hostname label emits a synthesized dialable identity", func(t *testing.T) {
		var got []MDNSService
		mdnsStreamResolveAndEmit(context.Background(), browseResult{
			instanceName:  "valid-label",
			interfaceName: "en0",
		}, wendyServiceType, func(svc MDNSService) { got = append(got, svc) })

		if len(got) != 1 {
			t.Fatalf("got %d emissions, want 1: %+v", len(got), got)
		}
		svc := got[0]
		// Hostname/Port are what the pre-stream deviceFromBrowse fallback
		// synthesized. Without them the row has no identity at all: the mapper
		// derives ID/DisplayName from the hostname, so the device would reach
		// the engine nameless, un-dialable, and keyed by "" in the cache.
		if svc.InstanceName != "valid-label" || svc.InterfaceName != "en0" ||
			svc.Hostname != "valid-label.local" || svc.Port != defaultAgentPort || len(svc.TXTRecords) != 0 {
			t.Errorf("emitted %+v, want {InstanceName+Hostname valid-label(.local), Port %d, InterfaceName en0}", svc, defaultAgentPort)
		}
		if dev := lanDeviceFromService(svc); dev.ID == "" || dev.DisplayName == "" || dev.Port == 0 {
			t.Errorf("fallback sighting maps to an unusable device: %+v", dev)
		}
	})

	t.Run("instance name unusable as a hostname label is skipped", func(t *testing.T) {
		var got []MDNSService
		mdnsStreamResolveAndEmit(context.Background(), browseResult{
			instanceName:  "My Device", // space: not a valid RFC1123 label
			interfaceName: "en0",
		}, wendyServiceType, func(svc MDNSService) { got = append(got, svc) })

		if len(got) != 0 {
			t.Errorf("got %d emissions, want 0 (invalid label must not emit): %+v", len(got), got)
		}
	})

	t.Run("failed Wendy Lite resolve does not synthesize a ghost agent row", func(t *testing.T) {
		var got []MDNSService
		mdnsStreamResolveAndEmit(context.Background(), browseResult{
			instanceName:  "offline-esp32",
			interfaceName: "en0",
		}, "_wendy-lite._tcp", func(svc MDNSService) { got = append(got, svc) })

		if len(got) != 0 {
			t.Errorf("got %d emissions, want 0 for an unresolved non-WendyOS service: %+v", len(got), got)
		}
	})
}

func TestPreferInterfaceRoutedAddrAvoidsIPv4OnWrongInterface(t *testing.T) {
	original := routeInterfaceForMDNSAddressFn
	t.Cleanup(func() { routeInterfaceForMDNSAddressFn = original })
	routeInterfaceForMDNSAddressFn = func(addr string) string {
		switch addr {
		case "192.168.123.18":
			return "en0"
		case "fdde:2b9::18":
			return "bridge104"
		default:
			return ""
		}
	}

	got := preferInterfaceRoutedAddr([]string{"192.168.123.18", "fdde:2b9::18"}, "en7")
	if got != "fdde:2b9::18" {
		t.Fatalf("preferInterfaceRoutedAddr = %q, want Ethernet ULA instead of wrong-interface IPv4", got)
	}
}
