package commands

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func unresolvedCloudNameError() error {
	return &net.DNSError{Name: "ci-mesh-2", Err: "no such host", IsNotFound: true}
}

func TestNamedCloudDeviceResolvesThroughBothAgentEntryPoints(t *testing.T) {
	for _, entry := range []string{"connectToAgent", "resolveTarget"} {
		t.Run(entry, func(t *testing.T) {
			restoreDeviceGlobals(t)
			stubNonInteractive(t)
			setPinCache(t)
			writePinTestConfig(t, nil)
			t.Setenv("WENDY_AGENT_SOCKET", "")
			t.Setenv(runNoCloudFallbackEnv, "")
			deviceFlag = "ci-mesh-2"
			oldLookup, oldBrowse, oldDiscover := osLookupHostFn, lanBrowseFn, discoverLANDevices
			oldUSB, oldFind := usbDirectCandidatesFn, findDeviceByIDFn
			oldCloud := connectUnresolvedCloudNameFn
			t.Cleanup(func() {
				osLookupHostFn, lanBrowseFn, discoverLANDevices = oldLookup, oldBrowse, oldDiscover
				usbDirectCandidatesFn, findDeviceByIDFn = oldUSB, oldFind
				connectUnresolvedCloudNameFn = oldCloud
			})
			osLookupHostFn = func(context.Context, string) ([]string, error) { return nil, unresolvedCloudNameError() }
			lanBrowseFn = func(context.Context, time.Duration) ([]models.LANDevice, error) { return nil, nil }
			discoverLANDevices = lanBrowseFn
			usbDirectCandidatesFn = func() []discovery.USBDirectCandidate { return nil }
			findDeviceByIDFn = func(context.Context, string) *SelectedDevice { return nil }
			want := &grpcclient.AgentConnection{Host: "ci-mesh-2", IsMTLS: true}
			calls := 0
			connectUnresolvedCloudNameFn = func(_ context.Context, _, name, _ string, expected *certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
				calls++
				if name != deviceFlag || expected != nil {
					t.Fatalf("cloud selection = %q, %+v; want the explicitly named device", name, expected)
				}
				return want, nil
			}
			opts := []resolveOption{SuppressUpdateCheck(), SuppressProvisioningHint(), DisableSessionBroker(), NonInteractive(), ExcludeProviders("local")}
			var got *grpcclient.AgentConnection
			var err error
			if entry == "connectToAgent" {
				got, err = connectToAgent(context.Background(), opts...)
			} else {
				var selected *SelectedDevice
				selected, err = resolveTarget(context.Background(), opts...)
				if selected != nil {
					got = selected.Agent
				}
			}
			if err != nil || got != want || calls != 1 {
				t.Fatalf("connection = %p, %v; cloud calls=%d", got, err, calls)
			}
		})
	}
}

func TestUnresolvedCloudNamePreservesPinnedIdentityAndCloudErrors(t *testing.T) {
	setPinCache(t)
	writePinTestConfig(t, map[string]config.DevicePin{
		"ci-mesh-2": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	t.Setenv(runNoCloudFallbackEnv, "")
	old := connectUnresolvedCloudNameFn
	t.Cleanup(func() { connectUnresolvedCloudNameFn = old })
	for _, want := range []error{
		&errCloudDeviceNotFound{name: "ci-mesh-2"},
		commandErrorf(errDeviceAmbiguous, "multiple devices match"),
		commandErrorf(errDeviceOffline, "device is enrolled but offline"),
		refuseIdentity("saved device identity changed"),
	} {
		connectUnresolvedCloudNameFn = func(_ context.Context, _, name, _ string, expected *certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
			if name != "42" || expected == nil || expected.EntityID != "42" || expected.OrgID != 7 {
				t.Fatalf("cloud selection = %q, %+v; want pinned asset 42 in org 7", name, expected)
			}
			return nil, want
		}
		conn, err, attempted := connectUnresolvedCloudName(context.Background(), "ci-mesh-2", unresolvedCloudNameError())
		if conn != nil || !attempted || !errors.Is(err, want) {
			t.Fatalf("connection = %v, %v, attempted=%v; want original Cloud error %v", conn, err, attempted, want)
		}
	}
}

func TestUnresolvedCloudNameNeverReinterpretsEndpointsOrRefusals(t *testing.T) {
	setTempConfig(t, &config.Config{Auth: []config.AuthConfig{{CloudGRPC: "cloud.example:443"}}})
	t.Setenv(runNoCloudFallbackEnv, "")
	old := connectUnresolvedCloudNameFn
	t.Cleanup(func() { connectUnresolvedCloudNameFn = old })
	connectUnresolvedCloudNameFn = func(context.Context, string, string, string, *certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
		t.Fatal("Cloud must not replace the requested endpoint or bypass a refusal")
		return nil, nil
	}
	for _, device := range []string{"", "192.168.100.111", "ci-mesh-2:50051", "ci-mesh-2.local", "ci-mesh-2.example.com", "[::1]:50051", "vm:ci-mesh-2", "cloud://cloud.example/tenant/t/asset/a"} {
		if _, _, attempted := connectUnresolvedCloudName(context.Background(), device, unresolvedCloudNameError()); attempted {
			t.Errorf("attempted Cloud lookup for %q", device)
		}
	}
	for _, cause := range []error{context.Canceled, errDeviceIdentityRefused, errProvisionedAgentUnauthorized, errTLSHandshakeRejected} {
		// Include a resolver miss to prove the refusal wins over that evidence.
		if _, _, attempted := connectUnresolvedCloudName(context.Background(), "ci-mesh-2", errors.Join(cause, unresolvedCloudNameError())); attempted {
			t.Errorf("attempted Cloud lookup after %v", cause)
		}
	}
	if _, _, attempted := connectUnresolvedCloudName(context.Background(), "ci-mesh-2", errors.New("connection refused")); attempted {
		t.Error("a failed resolved endpoint fell back to Cloud")
	}
	t.Setenv(runNoCloudFallbackEnv, "1")
	if _, _, attempted := connectUnresolvedCloudName(context.Background(), "ci-mesh-2", unresolvedCloudNameError()); attempted {
		t.Error("MCP-pinned target fell back to Cloud")
	}
}
