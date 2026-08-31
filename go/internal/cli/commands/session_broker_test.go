package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
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
