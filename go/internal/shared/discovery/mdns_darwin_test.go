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
