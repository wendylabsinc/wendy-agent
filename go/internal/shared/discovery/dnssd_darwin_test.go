//go:build darwin

package discovery

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestDNSSDBrowseAndResolve exercises the cgo binding end to end against
// mDNSResponder: the C trampolines, the session handle registry, and the socket
// pump. It registers its own service, so it needs no device on the network.
//
// The assertions are the two things the binding can get wrong without failing
// loudly: the port arrives in network byte order and must be swapped, and TXT
// values are length-prefixed records that must survive spaces intact — the case
// the previous implementation recovered by unescaping dns-sd's display output.
func TestDNSSDBrowseAndResolve(t *testing.T) {
	// A service type of its own, so a browse cannot pick up real devices, and a
	// pid in the instance name so concurrent test binaries do not collide and
	// trigger mDNSResponder's conflict renaming.
	const serviceType = "_wendy-selftest._tcp"
	instance := fmt.Sprintf("wendy-selftest-%d", os.Getpid())

	want := map[string]string{
		"displayname": "Tom Rpi4",
		"assetid":     "338",
		"tls":         "true",
	}
	stop, err := dnssdRegister(instance, serviceType, 51234, want)
	if err != nil {
		t.Skipf("cannot register an mDNS service (is mDNSResponder reachable?): %v", err)
	}
	t.Cleanup(stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	results, err := dnssdBrowse(ctx, serviceType)
	if err != nil {
		t.Fatalf("dnssdBrowse: %v", err)
	}
	var inst browseResult
	for _, r := range results {
		if r.instanceName == instance {
			inst = r
			break
		}
	}
	if inst.instanceName == "" {
		t.Fatalf("browse did not return %q; got %+v", instance, results)
	}

	hostname, port, txt, err := dnssdResolveInstance(ctx, inst, serviceType)
	if err != nil {
		t.Fatalf("dnssdResolveInstance: %v", err)
	}
	if hostname == "" {
		t.Error("resolve returned an empty hostname")
	}
	if port != 51234 {
		t.Errorf("port = %d, want 51234", port)
	}
	for k, v := range want {
		if txt[k] != v {
			t.Errorf("TXT[%q] = %q, want %q", k, txt[k], v)
		}
	}
}
