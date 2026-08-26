package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestConnectPinnedSessionUsesExactConfiguredAsset(t *testing.T) {
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
	connectSessionBrokerFn = func(_ context.Context, key string, expected certs.WendyIdentity) (*grpcclient.AgentConnection, error) {
		if key != "orin.local" {
			t.Fatalf("key = %q", key)
		}
		if expected != (certs.WendyIdentity{OrgID: 7, EntityType: "asset", EntityID: "42"}) {
			t.Fatalf("expected identity = %+v", expected)
		}
		return want, nil
	}

	got, ok := connectPinnedSession(context.Background(), "orin.local", "orin.local:50051")
	if !ok || got != want {
		t.Fatalf("connectPinnedSession = (%p, %v), want (%p, true)", got, ok, want)
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

	if conn, ok := connectPinnedSession(context.Background(), "new.local", "new.local:50051"); ok || conn != nil {
		t.Fatalf("connectPinnedSession = (%v, %v), want unavailable", conn, ok)
	}
}
