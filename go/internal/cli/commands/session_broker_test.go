package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestConnectPinnedSessionKeysBrokerByRequestedEndpoint(t *testing.T) {
	oldLoad, oldConnect := loadConfigForPinFn, connectSessionBrokerFn
	t.Cleanup(func() {
		loadConfigForPinFn = oldLoad
		connectSessionBrokerFn = oldConnect
	})
	loadConfigForPinFn = func() (*config.Config, error) {
		return &config.Config{DevicePins: map[string]config.DevicePin{
			"orin": {OrgID: 7, AssetID: "42"},
		}}, nil
	}
	want := &grpcclient.AgentConnection{}
	var gotKeys []string
	connectSessionBrokerFn = func(_ context.Context, key string, expected certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
		gotKeys = append(gotKeys, key)
		if expected != (certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}) {
			t.Fatalf("expected identity = %+v", expected)
		}
		return want, nil
	}

	got, ok := connectPinnedSession(context.Background(), "orin.local:50051")
	if !ok || got != want {
		t.Fatalf("connectPinnedSession = (%p, %v), want (%p, true)", got, ok, want)
	}
	// A dev agent on :51000 beside the production agent on the default port
	// presents the same asset certificate, so the identity check cannot tell
	// them apart — only the endpoint in the key can. Host-only keys let an
	// explicit-port request hit (and mutate) the wrong agent.
	if _, ok := connectPinnedSession(context.Background(), "orin.local:51000"); !ok {
		t.Fatal("explicit-port consult failed")
	}
	if len(gotKeys) != 2 || gotKeys[0] != "orin.local:50051" || gotKeys[1] != "orin.local:51000" {
		t.Fatalf("broker keys = %q, want the full requested endpoints", gotKeys)
	}
}

// A broker hit is a liveness-proven connect like any other, and every
// proof-of-life exit must refresh the discovery/LKG cache entry (see the
// comment above cacheConnectSuccess): without it a developer broker-hitting
// every ~90s for two hours watches LastSeen age past the freshness horizon
// despite continuous successful connects, pickers go stale, and the
// post-broker cold start loses the fast-connect quality the cache exists for.
func TestResolveTargetBrokerHitRefreshesDeviceCache(t *testing.T) {
	stubNonInteractive(t)
	writePinTestConfig(t, map[string]config.DevicePin{
		"orin": {OrgID: 7, CloudGRPC: "grpc.a.sh:443", AssetID: "42"},
	})
	path := filepath.Join(t.TempDir(), "devices.json")
	seedDeviceCache(t, path, discoverycache.Entry{ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.5", Port: 50052, MTLS: true})
	origCache, origConnect, origUpdate := deviceCacheLoadFn, connectSessionBrokerFn, checkAndOfferUpdateFn
	t.Cleanup(func() {
		deviceCacheLoadFn, connectSessionBrokerFn, checkAndOfferUpdateFn = origCache, origConnect, origUpdate
	})
	deviceCacheLoadFn = func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }
	identity := certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}
	connectSessionBrokerFn = func(ctx context.Context, _ string, expected certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
		if expected != identity {
			t.Fatalf("expected identity = %+v", expected)
		}
		// The broker's device moved to a new DHCP lease since the cache entry.
		return grpcclient.ConnectSessionProxy(ctx, filepath.Join(t.TempDir(), "unused.sock"), "10.0.0.9", "10.0.0.9:50052", &config.CertificateInfo{OrganizationID: 7}, identity)
	}
	checkAndOfferUpdateFn = func(_ context.Context, conn *grpcclient.AgentConnection) (*grpcclient.AgentConnection, error) {
		return conn, nil
	}

	sel, err := resolveTargetInner(context.Background(), SelectDevice("orin.local"))
	if err != nil {
		t.Fatalf("resolveTargetInner: %v", err)
	}
	defer sel.Agent.Close()
	if !sel.Agent.IsSessionProxy {
		t.Fatal("expected the broker connection to be used")
	}

	after, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := cachedDeviceEntry(after, "orin.local")
	if !ok {
		t.Fatal("cache entry missing after broker hit")
	}
	if e.IP != "10.0.0.9" || e.Port != 50052 || !e.MTLS {
		t.Fatalf("cache entry = {IP:%s Port:%d MTLS:%v}, want the broker session's endpoint {IP:10.0.0.9 Port:50052 MTLS:true} refreshed in", e.IP, e.Port, e.MTLS)
	}
}

func TestConnectPinnedSessionSkipsUnpinnedDevice(t *testing.T) {
	oldLoad, oldConnect := loadConfigForPinFn, connectSessionBrokerFn
	t.Cleanup(func() {
		loadConfigForPinFn = oldLoad
		connectSessionBrokerFn = oldConnect
	})
	loadConfigForPinFn = func() (*config.Config, error) { return &config.Config{}, nil }
	connectSessionBrokerFn = func(context.Context, string, certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
		t.Fatal("broker must not be consulted without an exact device pin")
		return nil, nil
	}

	if conn, ok := connectPinnedSession(context.Background(), "new.local:50051"); ok || conn != nil {
		t.Fatalf("connectPinnedSession = (%v, %v), want unavailable", conn, ok)
	}
}
