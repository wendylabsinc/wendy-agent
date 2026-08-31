package containerd

import (
	"context"
	"reflect"
	"testing"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// TestCameraConsumerNames_FiltersEntitlementAndRunning covers the pure
// filter cameraConsumerNames applies to raw container truth: camera and the
// deprecated video alias (both map to applyCamera, entitlements.go:83) count
// as camera consumers; no entitlement, a stopped task, or labels that fail
// the same appID/serviceName revalidation appSystemAPIOwnersFromLabels
// applies (client.go:204-217) are all excluded.
func TestCameraConsumerNames_FiltersEntitlementAndRunning(t *testing.T) {
	cameraLabels := map[string]string{
		labelKeyAppID:                 "camapp",
		"sh.wendy/entitlement.camera": `{"mode":"detect"}`,
	}
	videoLabels := map[string]string{
		labelKeyAppID:                "videoapp",
		"sh.wendy/entitlement.video": `{"mode":"detect"}`,
	}
	noneLabels := map[string]string{
		labelKeyAppID: "plainapp",
	}
	invalidAppIDLabels := map[string]string{
		labelKeyAppID:                 "bad app id!",
		"sh.wendy/entitlement.camera": `{"mode":"detect"}`,
	}

	tests := []struct {
		name  string
		infos []containerCameraInfo
		want  []string
	}{
		{
			name: "camera entitlement, running -> in",
			infos: []containerCameraInfo{
				{name: "camapp", labels: cameraLabels, running: true},
			},
			want: []string{"camapp"},
		},
		{
			name: "deprecated video entitlement, running -> in",
			infos: []containerCameraInfo{
				{name: "videoapp", labels: videoLabels, running: true},
			},
			want: []string{"videoapp"},
		},
		{
			name: "no camera/video entitlement -> out",
			infos: []containerCameraInfo{
				{name: "plainapp", labels: noneLabels, running: true},
			},
			want: nil,
		},
		{
			name: "camera entitlement but stopped -> out",
			infos: []containerCameraInfo{
				{name: "camapp", labels: cameraLabels, running: false},
			},
			want: nil,
		},
		{
			name: "invalid appID label -> out",
			infos: []containerCameraInfo{
				{name: "brokenapp", labels: invalidAppIDLabels, running: true},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cameraConsumerNames(tt.infos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("cameraConsumerNames() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestSyncCameraLoopbacks_NilProviderNoop verifies the guard that keeps a
// Client built without SetCameraLoopbackProvider (the common case for unit
// tests, and any build without the ipcam module) from touching containerd at
// all — it must return immediately rather than panicking on a nil c.client.
func TestSyncCameraLoopbacks_NilProviderNoop(t *testing.T) {
	c := &Client{logger: zap.NewNop()}

	// Must not panic despite c.client being nil: the nil-provider check has
	// to come before any containerd call.
	c.SyncCameraLoopbacks(context.Background())
}

// TestShouldEnsureCameraNodes covers the create-hook gating predicate: only
// the camera entitlement and its deprecated video alias should trigger
// EnsureCameraNodes before ApplyEntitlements runs (see the create-hook
// comment in CreateContainerWithProgress, citing applyCamera's /dev
// bind-mount + major-81 minor-unrestricted rationale).
func TestShouldEnsureCameraNodes(t *testing.T) {
	tests := []struct {
		name   string
		appCfg *appconfig.AppConfig
		want   bool
	}{
		{
			name:   "nil appCfg",
			appCfg: nil,
			want:   false,
		},
		{
			name:   "camera entitlement",
			appCfg: &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementCamera}}},
			want:   true,
		},
		{
			name:   "deprecated video entitlement",
			appCfg: &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementVideo}}},
			want:   true,
		},
		{
			name:   "unrelated entitlement only",
			appCfg: &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementGPU}}},
			want:   false,
		},
		{
			name:   "no entitlements",
			appCfg: &appconfig.AppConfig{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldEnsureCameraNodes(tt.appCfg)
			if got != tt.want {
				t.Errorf("shouldEnsureCameraNodes(%+v) = %v, want %v", tt.appCfg, got, tt.want)
			}
		})
	}
}

// TestTwoPlaneConsumerNames_RequiresBothEntitlements pins the entitlement
// decision for the two-plane camera path: a container qualifies only when it
// holds BOTH sensors and camera (or the deprecated video alias).
//
// The two negative cases in the middle are the ones that matter. Sensors alone
// must not qualify, because qualifying would cause a readable /dev/video* node
// to be created for an entitlement that documents itself as granting no device
// nodes. Camera alone must not qualify either, because a hub-fed node without
// the identity stream gives an app the same unprovable frame join it already
// has, which is the defect the harness exists to remove.
func TestTwoPlaneConsumerNames_RequiresBothEntitlements(t *testing.T) {
	bothLabels := map[string]string{
		labelKeyAppID:                      "modelapp",
		"sh.wendy/entitlement.camera":      `{"mode":"detect"}`,
		"sh.wendy/entitlement.sensor-read": `{}`,
	}
	bothViaVideoAlias := map[string]string{
		labelKeyAppID:                      "aliasapp",
		"sh.wendy/entitlement.video":       `{"mode":"detect"}`,
		"sh.wendy/entitlement.sensor-read": `{}`,
	}
	sensorsOnly := map[string]string{
		labelKeyAppID:                      "sensorapp",
		"sh.wendy/entitlement.sensor-read": `{}`,
	}
	cameraOnly := map[string]string{
		labelKeyAppID:                 "camapp",
		"sh.wendy/entitlement.camera": `{"mode":"detect"}`,
	}
	invalidAppID := map[string]string{
		labelKeyAppID:                      "bad app id!",
		"sh.wendy/entitlement.camera":      `{"mode":"detect"}`,
		"sh.wendy/entitlement.sensor-read": `{}`,
	}

	tests := []struct {
		name  string
		infos []containerCameraInfo
		want  []string
	}{
		{
			name:  "sensors and camera, running -> in",
			infos: []containerCameraInfo{{name: "modelapp", labels: bothLabels, running: true}},
			want:  []string{"modelapp"},
		},
		{
			name:  "sensors and deprecated video alias, running -> in",
			infos: []containerCameraInfo{{name: "aliasapp", labels: bothViaVideoAlias, running: true}},
			want:  []string{"aliasapp"},
		},
		{
			// Qualifying here would silently turn sensors into a device-node grant.
			name:  "sensors alone -> out",
			infos: []containerCameraInfo{{name: "sensorapp", labels: sensorsOnly, running: true}},
			want:  nil,
		},
		{
			// Qualifying here would hand over pixels with no way to name them.
			name:  "camera alone -> out",
			infos: []containerCameraInfo{{name: "camapp", labels: cameraOnly, running: true}},
			want:  nil,
		},
		{
			name:  "both entitlements but stopped -> out",
			infos: []containerCameraInfo{{name: "modelapp", labels: bothLabels, running: false}},
			want:  nil,
		},
		{
			name:  "invalid appID label -> out",
			infos: []containerCameraInfo{{name: "brokenapp", labels: invalidAppID, running: true}},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := twoPlaneConsumerNames(tt.infos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("twoPlaneConsumerNames() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestTwoPlaneConsumerNames_IsSubsetOfCameraConsumers pins the containment
// relation the design depends on: every two-plane consumer is also a camera
// consumer, so the narrower path can never entitle a container the existing
// camera path would not.
func TestTwoPlaneConsumerNames_IsSubsetOfCameraConsumers(t *testing.T) {
	infos := []containerCameraInfo{
		{name: "both", labels: map[string]string{
			labelKeyAppID:                      "both",
			"sh.wendy/entitlement.camera":      `{"mode":"detect"}`,
			"sh.wendy/entitlement.sensor-read": `{}`,
		}, running: true},
		{name: "camonly", labels: map[string]string{
			labelKeyAppID:                 "camonly",
			"sh.wendy/entitlement.camera": `{"mode":"detect"}`,
		}, running: true},
		{name: "sensoronly", labels: map[string]string{
			labelKeyAppID:                      "sensoronly",
			"sh.wendy/entitlement.sensor-read": `{}`,
		}, running: true},
	}

	cameras := map[string]bool{}
	for _, n := range cameraConsumerNames(infos) {
		cameras[n] = true
	}
	for _, n := range twoPlaneConsumerNames(infos) {
		if !cameras[n] {
			t.Errorf("%q is a two-plane consumer but not a camera consumer; the two-plane "+
				"path must never entitle a container the camera path would not", n)
		}
	}
}
