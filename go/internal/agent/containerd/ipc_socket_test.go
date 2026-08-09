package containerd

import (
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// The concrete manager must keep satisfying the narrow interface the client
// depends on, so a signature change is caught here rather than in main.go.
var _ IPCSocketProvider = (*services.IPCSocketManager)(nil)

// ipcEntitlements is fed from container labels, which are external state.
// Anything malformed must be dropped rather than reaching the manager, where
// the name would become a host path component.
func TestIPCEntitlements_DropsMalformedEntries(t *testing.T) {
	got := ipcEntitlements([]appconfig.Entitlement{
		{Type: appconfig.EntitlementIPC, Name: "world", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "planner", Role: appconfig.IPCRoleConsume},
		{Type: appconfig.EntitlementPersist, Name: "data", Path: "/data"},
		{Type: appconfig.EntitlementIPC, Name: "../etc", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "..", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "World", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "world2", Role: ""},
		{Type: appconfig.EntitlementIPC, Name: "world3", Role: "publish"},
	})
	want := []appconfig.Entitlement{
		{Type: appconfig.EntitlementIPC, Name: "world", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "planner", Role: appconfig.IPCRoleConsume},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ipcEntitlements = %+v, want %+v", got, want)
	}
}

func TestIPCEntitlements_NoneWhenAbsent(t *testing.T) {
	if got := ipcEntitlements(nil); got != nil {
		t.Errorf("ipcEntitlements(nil) = %+v, want nil", got)
	}
	if got := ipcEntitlements([]appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}); got != nil {
		t.Errorf("ipcEntitlements(gpu) = %+v, want nil", got)
	}
}

// IPC entitlements must survive the round trip through container labels:
// the start-time stale-socket cleanup and the delete-time claim release both
// read them back from labels rather than from the original request.
func TestIPCEntitlements_SurviveLabelRoundTrip(t *testing.T) {
	want := []appconfig.Entitlement{
		{Type: appconfig.EntitlementIPC, Name: "world", Role: appconfig.IPCRoleProvide},
		{Type: appconfig.EntitlementIPC, Name: "planner", Role: appconfig.IPCRoleConsume},
	}
	labels := wendyLabels("com.example.app", "api", "1", nil, want, "", nil)
	got := ipcEntitlements(parseEntitlementsFromAnnotations(labels))

	if len(got) != len(want) {
		t.Fatalf("round-tripped %d ipc entitlements, want %d: %+v", len(got), len(want), got)
	}
	byName := make(map[string]appconfig.Entitlement, len(got))
	for _, e := range got {
		byName[e.Name] = e
	}
	for _, w := range want {
		if !reflect.DeepEqual(byName[w.Name], w) {
			t.Errorf("ipc name %q round-tripped as %+v, want %+v", w.Name, byName[w.Name], w)
		}
	}
}

// A client with no ipc socket provider wired in (unit tests, or an agent
// build without one) must not panic on the release paths.
func TestReleaseIPCSockets_NilProviderIsNoOp(t *testing.T) {
	c := &Client{}
	c.releaseIPCSockets([]appconfig.Entitlement{
		{Type: appconfig.EntitlementIPC, Name: "world", Role: appconfig.IPCRoleProvide},
	}, "com.example.app", "")
}
