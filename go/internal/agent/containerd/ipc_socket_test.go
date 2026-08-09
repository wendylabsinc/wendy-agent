package containerd

import (
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// The concrete manager must keep satisfying the narrow interface the client
// depends on, so a signature change is caught here rather than in main.go.
var _ ServiceSocketProvider = (*services.ServiceSocketManager)(nil)

// serviceEntitlements is fed from container labels, which are external state.
// Anything malformed must be dropped rather than reaching the manager, where
// the name would become a host path component.
func TestServiceEntitlements_DropsMalformedEntries(t *testing.T) {
	got := serviceEntitlements([]appconfig.Entitlement{
		{Type: appconfig.EntitlementService, Name: "world", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "planner", Role: appconfig.ServiceRoleConsume},
		{Type: appconfig.EntitlementPersist, Name: "data", Path: "/data"},
		{Type: appconfig.EntitlementService, Name: "../etc", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "..", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "World", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "world2", Role: ""},
		{Type: appconfig.EntitlementService, Name: "world3", Role: "publish"},
	})
	want := []appconfig.Entitlement{
		{Type: appconfig.EntitlementService, Name: "world", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "planner", Role: appconfig.ServiceRoleConsume},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceEntitlements = %+v, want %+v", got, want)
	}
}

func TestServiceEntitlements_NoneWhenAbsent(t *testing.T) {
	if got := serviceEntitlements(nil); got != nil {
		t.Errorf("serviceEntitlements(nil) = %+v, want nil", got)
	}
	if got := serviceEntitlements([]appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}); got != nil {
		t.Errorf("serviceEntitlements(gpu) = %+v, want nil", got)
	}
}

// Service entitlements must survive the round trip through container labels:
// the start-time stale-socket cleanup and the delete-time claim release both
// read them back from labels rather than from the original request.
func TestServiceEntitlements_SurviveLabelRoundTrip(t *testing.T) {
	want := []appconfig.Entitlement{
		{Type: appconfig.EntitlementService, Name: "world", Role: appconfig.ServiceRoleProvide},
		{Type: appconfig.EntitlementService, Name: "planner", Role: appconfig.ServiceRoleConsume},
	}
	labels := wendyLabels("com.example.app", "api", "1", nil, want, "", nil)
	got := serviceEntitlements(parseEntitlementsFromAnnotations(labels))

	if len(got) != len(want) {
		t.Fatalf("round-tripped %d service entitlements, want %d: %+v", len(got), len(want), got)
	}
	byName := make(map[string]appconfig.Entitlement, len(got))
	for _, e := range got {
		byName[e.Name] = e
	}
	for _, w := range want {
		if !reflect.DeepEqual(byName[w.Name], w) {
			t.Errorf("service %q round-tripped as %+v, want %+v", w.Name, byName[w.Name], w)
		}
	}
}

// A client with no service socket provider wired in (unit tests, or an agent
// build without one) must not panic on the release paths.
func TestReleaseServiceSockets_NilProviderIsNoOp(t *testing.T) {
	c := &Client{}
	c.releaseServiceSockets([]appconfig.Entitlement{
		{Type: appconfig.EntitlementService, Name: "world", Role: appconfig.ServiceRoleProvide},
	}, "com.example.app", "")
}
