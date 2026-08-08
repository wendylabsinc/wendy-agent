package commands

import (
	"context"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
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
	// Port is the ADVERTISED mTLS port (50052), matching what an mDNS-resolved
	// provisioned device carries — see TestProbedUSBDeviceDialsPlaintextPort.
	if d.Hostname != "wendy-orin.local" || d.DisplayName != "wendy-orin" ||
		d.IPAddress != "fe80::5741:1%enx001122334455" || d.Port != 50052 ||
		!d.IsMTLS || d.USB == "" || d.NetworkInterface != "enx001122334455" ||
		!d.IsWendyDevice || d.AgentVersion != "1.2.3" || d.InterfaceType != string(models.InterfaceLAN) {
		t.Fatalf("unexpected device: %+v", d)
	}
}

// A probe-built device is fed straight into the picker and the connect path, so
// its Port must survive the mTLS→plaintext offset math in lanAgentAddresses and
// land back on the port the probe actually reached the agent on.
func TestProbedUSBDeviceDialsPlaintextPort(t *testing.T) {
	for _, isMTLS := range []bool{true, false} {
		withUSBDirectStubs(t,
			[]discovery.USBDirectCandidate{{Interface: "enxa", Zone: "enxa"}},
			func(context.Context, string) (bool, *agentpb.GetAgentVersionResponse, error) {
				return isMTLS, &agentpb.GetAgentVersionResponse{Hostname: "wendy-orin"}, nil
			})

		devs := probeUSBDirectDevices(context.Background())
		if len(devs) != 1 {
			t.Fatalf("isMTLS=%v: got %d devices, want 1", isMTLS, len(devs))
		}
		got := lanAgentAddresses(devs[0])
		if len(got) != 2 {
			t.Fatalf("isMTLS=%v: lanAgentAddresses() = %v, want the link-local and .local addresses", isMTLS, got)
		}
		for _, addr := range got {
			if !strings.HasSuffix(addr, ":50051") {
				t.Fatalf("isMTLS=%v: address %q must dial the plaintext port 50051", isMTLS, addr)
			}
		}
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

// fakeVersionClient overrides only GetAgentVersion; every other method of the
// embedded (nil) interface panics if called, which is what we want in tests.
type fakeVersionClient struct {
	agentpb.WendyAgentServiceClient
	resp *agentpb.GetAgentVersionResponse
	err  error
}

func (f fakeVersionClient) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest, ...grpc.CallOption) (*agentpb.GetAgentVersionResponse, error) {
	return f.resp, f.err
}

// recordingCloser observes whether Close was called on it. Attaching one via
// AgentConnection.ExtraClosers makes AgentConnection.Close() observable in
// tests: with a nil Conn and no ExtraClosers, Close() silently no-ops, so a
// bare fake connection can't prove a mismatched/errored fallback candidate
// was actually closed.
type recordingCloser struct{ closed bool }

func (r *recordingCloser) Close() error { r.closed = true; return nil }

func TestUSBDirectFallbackMatchesHostname(t *testing.T) {
	withUSBDirectStubs(t,
		[]discovery.USBDirectCandidate{{Interface: "enxa", Zone: "enxa"}},
		getAgentVersionAtAddress) // unused by fallback; keep original semantics

	rec := &recordingCloser{}
	origConnect := usbDirectConnectFn
	usbDirectConnectFn = func(_ context.Context, addr string) (*grpcclient.AgentConnection, error) {
		if addr != "[fe80::5741:1%enxa]:50051" {
			t.Errorf("dial addr = %q", addr)
		}
		return &grpcclient.AgentConnection{
			AgentService: fakeVersionClient{resp: &agentpb.GetAgentVersionResponse{Hostname: "wendy-orin"}},
			ExtraClosers: []io.Closer{rec},
		}, nil
	}
	t.Cleanup(func() { usbDirectConnectFn = origConnect })

	conn, ok := usbDirectFallback(context.Background(), "wendy-orin.local")
	if !ok || conn == nil {
		t.Fatal("expected a matched connection")
	}
	if rec.closed {
		t.Fatal("a matched connection must be returned live, not closed")
	}
}

func TestUSBDirectFallbackRejectsWrongOrUnknownHostname(t *testing.T) {
	for name, resp := range map[string]*agentpb.GetAgentVersionResponse{
		"wrong device": {Hostname: "wendy-pi"},
		"old agent":    {Hostname: ""},
	} {
		t.Run(name, func(t *testing.T) {
			withUSBDirectStubs(t,
				[]discovery.USBDirectCandidate{{Interface: "enxa", Zone: "enxa"}},
				getAgentVersionAtAddress)
			rec := &recordingCloser{}
			origConnect := usbDirectConnectFn
			usbDirectConnectFn = func(context.Context, string) (*grpcclient.AgentConnection, error) {
				return &grpcclient.AgentConnection{AgentService: fakeVersionClient{resp: resp}, ExtraClosers: []io.Closer{rec}}, nil
			}
			t.Cleanup(func() { usbDirectConnectFn = origConnect })

			if _, ok := usbDirectFallback(context.Background(), "wendy-orin.local"); ok {
				t.Fatal("must not connect to a device with a different or unknown hostname")
			}
			if !rec.closed {
				t.Fatal("rejected connection must be closed")
			}
		})
	}
}

func TestUSBDirectFallbackNoCandidates(t *testing.T) {
	withUSBDirectStubs(t, nil, getAgentVersionAtAddress)
	if _, ok := usbDirectFallback(context.Background(), "wendy-orin.local"); ok {
		t.Fatal("no candidates must mean no fallback")
	}
}
