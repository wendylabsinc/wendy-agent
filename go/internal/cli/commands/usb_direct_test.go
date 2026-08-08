package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func withUSBDirectStubs(t *testing.T, cands []discovery.USBDirectCandidate,
	probe func(ctx context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error)) {
	t.Helper()
	origCands, origProbe := usbDirectCandidatesFn, getAgentVersionAtAddress
	usbDirectCandidatesFn = func() []discovery.USBDirectCandidate { return cands }
	getAgentVersionAtAddress = probe
	t.Cleanup(func() { usbDirectCandidatesFn, getAgentVersionAtAddress = origCands, origProbe })
}

func TestProbeUSBDirectDevicesBuildsLANDevice(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enx001122334455", Zone: "enx001122334455"}},
		func(_ context.Context, address string) (bool, *agentpb.GetAgentVersionResponse, error) {
			want := "[fe80::5741:1%enx001122334455]:50051"
			if address != want {
				t.Errorf("probe address = %q, want %q", address, want)
			}
			return true, &agentpb.GetAgentVersionResponse{Hostname: "wendy-orin", Version: "1.2.3", Os: "linux"}, nil
		})

	devs := probeUSBDirectDevices(context.Background())
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1", len(devs))
	}
	d := devs[0]
	if d.Hostname != "wendy-orin.local" || d.DisplayName != "wendy-orin" ||
		d.IPAddress != "fe80::5741:1%enx001122334455" || d.Port != 50051 ||
		!d.IsMTLS || d.USB == "" || d.NetworkInterface != "enx001122334455" ||
		!d.IsWendyDevice || d.AgentVersion != "1.2.3" || d.InterfaceType != string(models.InterfaceLAN) {
		t.Fatalf("unexpected device: %+v", d)
	}
}

func TestProbeUSBDirectDevicesSkipsNonAnswering(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enxdead", Zone: "enxdead"}},
		func(context.Context, string) (bool, *agentpb.GetAgentVersionResponse, error) {
			return false, nil, context.DeadlineExceeded
		})
	if devs := probeUSBDirectDevices(context.Background()); len(devs) != 0 {
		t.Fatalf("got %d devices, want 0", len(devs))
	}
}

func TestProbeUSBDirectDevicesNoCandidatesDialsNothing(t *testing.T) {
	withUSBDirectStubs(t, nil,
		func(context.Context, string) (bool, *agentpb.GetAgentVersionResponse, error) {
			t.Fatal("probe must not be called with no candidates")
			return false, nil, nil
		})
	if devs := probeUSBDirectDevices(context.Background()); devs != nil {
		t.Fatalf("got %v, want nil", devs)
	}
}

func TestMergeUSBDirectDevices(t *testing.T) {
	existing := []models.LANDevice{
		{Hostname: "wendy-orin.local", IPAddress: "192.168.1.50", Port: 50051, ID: "abc123"},
		{Hostname: "wendy-pi.local", IPAddress: "192.168.1.60", Port: 50051},
	}
	probed := []models.LANDevice{
		// Matches wendy-orin by hostname → enriches, does not duplicate.
		{Hostname: "wendy-orin.local", DisplayName: "wendy-orin", IPAddress: "fe80::5741:1%enxa", NetworkInterface: "enxa", USB: "enxa", Port: 50051},
		// No mDNS counterpart → appended.
		{Hostname: "wendy-thor.local", DisplayName: "wendy-thor", IPAddress: "fe80::5741:1%enxb", NetworkInterface: "enxb", USB: "enxb", Port: 50051},
	}

	got := mergeUSBDirectDevices(existing, probed)
	if len(got) != 3 {
		t.Fatalf("got %d devices, want 3: %+v", len(got), got)
	}
	orin := got[0]
	if orin.ID != "abc123" || orin.USB != "enxa" || orin.NetworkInterface != "enxa" {
		t.Fatalf("orin not enriched: %+v", orin)
	}
	if orin.IPAddress != "192.168.1.50" {
		t.Fatalf("mDNS-discovered address must be preserved, got %q", orin.IPAddress)
	}
	if got[2].Hostname != "wendy-thor.local" {
		t.Fatalf("probed-only device not appended: %+v", got[2])
	}
}

func TestShouldProbeUSBDirect(t *testing.T) {
	cases := []struct {
		types []models.InterfaceType
		want  bool
	}{
		{nil, true}, // "all types"
		{[]models.InterfaceType{models.InterfaceLAN}, true},
		{[]models.InterfaceType{models.InterfaceBluetooth}, false},
	}
	for _, c := range cases {
		if got := shouldProbeUSBDirect(discovery.DiscoveryOptions{Types: c.types}); got != c.want {
			t.Fatalf("shouldProbeUSBDirect(%v) = %v, want %v", c.types, got, c.want)
		}
	}
}

func TestResolveAddrOnceSkipsResolutionForZonedIPLiteral(t *testing.T) {
	origLookup := osLookupHostFn
	osLookupHostFn = func(context.Context, string) ([]string, error) {
		t.Fatal("resolver must not run for a zoned IPv6 literal")
		return nil, nil
	}
	t.Cleanup(func() { osLookupHostFn = origLookup })

	addr := "[fe80::5741:1%enx0]:50051"
	if got := resolveAddrOnce(context.Background(), addr); got != addr {
		t.Fatalf("resolveAddrOnce = %q, want unchanged %q", got, addr)
	}
}
