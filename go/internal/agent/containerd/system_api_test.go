package containerd

import (
	"errors"
	"reflect"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

type recordingAppSystemAPISocketProvider struct {
	ensures []appSystemAPIOwner
	err     error
}

func (p *recordingAppSystemAPISocketProvider) Ensure(appID, serviceName string, capabilities []string) (string, error) {
	p.ensures = append(p.ensures, appSystemAPIOwner{
		appID:        appID,
		serviceName:  serviceName,
		capabilities: append([]string(nil), capabilities...),
	})
	return "/var/lib/wendy/app-system/test", p.err
}

func (*recordingAppSystemAPISocketProvider) Release(string, string) {}
func (*recordingAppSystemAPISocketProvider) ReleaseApp(string)      {}

func TestAppSystemAPIOwnersFromLabelsRestoresEntitledMultiServiceContainers(t *testing.T) {
	notifications := []appconfig.Entitlement{{Type: appconfig.EntitlementNotifications}}
	camera := []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}
	combined := []appconfig.Entitlement{
		{Type: appconfig.EntitlementCamera},
		{Type: appconfig.EntitlementNotifications},
	}
	labels := []map[string]string{
		wendyLabels("com.example.app", "api", "1", nil, notifications, "", nil),
		wendyLabels("com.example.app", "worker", "1", nil, camera, "", nil),
		wendyLabels("com.example.combined", "", "1", nil, combined, "", nil),
		wendyLabels("com.example.other", "", "1", nil, nil, "", nil),
		{
			labelKeyAppID: "../tampered",
			appconfig.EntitlementAnnotationKeyPrefix + appconfig.EntitlementNotifications: "",
		},
	}

	got := appSystemAPIOwnersFromLabels(labels)
	want := []appSystemAPIOwner{
		{
			appID: "com.example.app", serviceName: "api",
			capabilities: []string{services.SystemAPICapabilityNotifications},
		},
		{
			appID: "com.example.app", serviceName: "worker",
			capabilities: []string{services.SystemAPICapabilityCamera},
		},
		{
			appID: "com.example.combined",
			capabilities: []string{
				services.SystemAPICapabilityCamera,
				services.SystemAPICapabilityNotifications,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("owners = %+v, want %+v", got, want)
	}
}

func TestEnsureAppSystemAPISocketForStartRestoresPersistedCameraOwner(t *testing.T) {
	provider := &recordingAppSystemAPISocketProvider{}
	client := &Client{systemAPISocketProvider: provider}
	labels := wendyLabels(
		"com.example.camera",
		"tracker",
		"1",
		nil,
		[]appconfig.Entitlement{{Type: appconfig.EntitlementCamera}},
		"",
		nil,
	)

	if err := client.ensureAppSystemAPISocketForStart(labels); err != nil {
		t.Fatalf("ensureAppSystemAPISocketForStart() error = %v", err)
	}
	want := []appSystemAPIOwner{{
		appID:        "com.example.camera",
		serviceName:  "tracker",
		capabilities: []string{services.SystemAPICapabilityCamera},
	}}
	if !reflect.DeepEqual(provider.ensures, want) {
		t.Fatalf("ensures = %+v, want %+v", provider.ensures, want)
	}
}

func TestEnsureAppSystemAPISocketForStartSkipsUnentitledContainer(t *testing.T) {
	client := &Client{}
	labels := wendyLabels("com.example.app", "", "1", nil, nil, "", nil)

	if err := client.ensureAppSystemAPISocketForStart(labels); err != nil {
		t.Fatalf("ensureAppSystemAPISocketForStart() error = %v", err)
	}
}

func TestEnsureAppSystemAPISocketForStartFailsClosed(t *testing.T) {
	wantErr := errors.New("socket unavailable")
	provider := &recordingAppSystemAPISocketProvider{err: wantErr}
	client := &Client{systemAPISocketProvider: provider}
	labels := wendyLabels(
		"com.example.camera",
		"",
		"1",
		nil,
		[]appconfig.Entitlement{{Type: appconfig.EntitlementCamera}},
		"",
		nil,
	)

	err := client.ensureAppSystemAPISocketForStart(labels)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ensureAppSystemAPISocketForStart() error = %v, want wrapped %v", err, wantErr)
	}
}
