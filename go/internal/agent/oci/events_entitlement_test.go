package oci

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestApplyEntitlementsEventsMountsOnlyNarrowSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "wendy-events-oci-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(directory)
	listener, err := net.Listen("unix", filepath.Join(directory, "events.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	spec := DefaultSpec("rootfs", []string{"/app"})
	cfg := &appconfig.AppConfig{
		AppID:        "dev.wendy.firewatch",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementEvents}},
	}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{EventSocketDir: directory}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}

	if !hasMount(spec, "/run/wendy/events") {
		t.Fatal("events entitlement did not mount its socket directory")
	}
	if hasMount(spec, "/run/wendy/agent") {
		t.Fatal("events entitlement must not expose the admin control socket")
	}
	if !hasEnv(spec, "WENDY_EVENT_SOCKET=/run/wendy/events/events.sock") {
		t.Fatal("events entitlement did not inject WENDY_EVENT_SOCKET")
	}
	var hasEventGID bool
	for _, gid := range spec.Process.User.AdditionalGids {
		if gid == appEventsGroupGID {
			hasEventGID = true
		}
	}
	if !hasEventGID {
		t.Fatal("events entitlement did not grant the narrow socket group")
	}
}
