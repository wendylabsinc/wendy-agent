//go:build darwin

package discovery

import (
	"context"
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

	// The same instance must not be streamed twice: the picker relies on dedup
	// to avoid a row per browse reply (mDNSResponder repeats them per interface).
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for svc := range ch {
			if svc.InstanceName == instance {
				t.Errorf("instance %q streamed more than once", instance)
			}
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
