package containerd

import (
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

func TestAppSystemAPIOwnersFromLabelsRestoresEntitledMultiServiceContainers(t *testing.T) {
	notifications := []appconfig.Entitlement{{Type: appconfig.EntitlementNotifications}}
	labels := []map[string]string{
		wendyLabels("com.example.app", "api", "1", nil, notifications, "", nil),
		wendyLabels("com.example.app", "worker", "1", nil, notifications, "", nil),
		wendyLabels("com.example.other", "", "1", nil, nil, "", nil),
		{
			labelKeyAppID: "../tampered",
			appconfig.EntitlementAnnotationKeyPrefix + appconfig.EntitlementNotifications: "",
		},
	}

	got := appSystemAPIOwnersFromLabels(labels)
	want := []appSystemAPIOwner{
		{appID: "com.example.app", serviceName: "api"},
		{appID: "com.example.app", serviceName: "worker"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owners = %+v, want %+v", got, want)
	}
}
