package containerd

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/dbusproxy"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestRequireDBusProxyRefusesBluetoothWithoutProxy is the regression test for
// WDY-1093: a container that declares the bluetooth (D-Bus) entitlement must be
// refused when xdg-dbus-proxy is unavailable. Without the proxy there is no way
// to scope D-Bus to org.bluez, so starting the container would silently break
// bluetooth (or, in older builds, expose the unfiltered system bus). The agent
// must fail loudly instead of degrading silently.
func TestRequireDBusProxyRefusesBluetoothWithoutProxy(t *testing.T) {
	client := &Client{logger: zap.NewNop(), proxyManager: nil}
	cfg := &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementBluetooth}}}
	if err := client.requireDBusProxy(cfg, "demo-app"); err == nil {
		t.Fatal("expected error when bluetooth entitlement is declared but xdg-dbus-proxy is unavailable")
	}
}

func TestRequireDBusProxyAllowsBluetoothWithProxy(t *testing.T) {
	client := &Client{logger: zap.NewNop(), proxyManager: dbusproxy.NewManager(zap.NewNop())}
	cfg := &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementBluetooth}}}
	if err := client.requireDBusProxy(cfg, "demo-app"); err != nil {
		t.Fatalf("expected no error when xdg-dbus-proxy is available; got %v", err)
	}
}

func TestRequireDBusProxyAllowsNonBluetoothWithoutProxy(t *testing.T) {
	client := &Client{logger: zap.NewNop(), proxyManager: nil}
	cfg := &appconfig.AppConfig{Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork}}}
	if err := client.requireDBusProxy(cfg, "demo-app"); err != nil {
		t.Fatalf("expected no error for a non-bluetooth app without the proxy; got %v", err)
	}
}

// TestROS2SidecarName maps each RMW to a distinct, prefix-scoped sidecar name
// and collapses empty RMW to the CycloneDDS (config-default) sidecar — the
// basis for one persistent sidecar per RMW (WDY-1594, WDY-1703).
func TestROS2SidecarName(t *testing.T) {
	cyc := ros2SidecarName("rmw_cyclonedds_cpp")
	fast := ros2SidecarName("rmw_fastrtps_cpp")
	def := ros2SidecarName("")
	if cyc == fast {
		t.Errorf("distinct RMWs must map to distinct sidecars: %q == %q", cyc, fast)
	}
	if def != cyc {
		t.Errorf("empty RMW should map to the CycloneDDS (config-default) sidecar (WDY-1703): %q vs %q", def, cyc)
	}
	if got := ros2SidecarName("rmw_bogus"); got != ros2SidecarPrefix+"-default" {
		t.Errorf("unknown RMW = %q, want %q", got, ros2SidecarPrefix+"-default")
	}
	for _, n := range []string{cyc, fast, def} {
		if !strings.HasPrefix(n, ros2SidecarPrefix+"-") {
			t.Errorf("sidecar name %q lacks the expected prefix", n)
		}
	}
}

func TestCreateContainerProgressMappingUsesApplyPhase(t *testing.T) {
	progress := UnpackProgress{
		Phase:       "layer",
		LayerIndex:  2,
		TotalLayers: 5,
		LayerSize:   1234,
		Reused:      true,
	}

	got := toCreateContainerProgress(progress)

	if got.GetPhase() != agentpb.CreateContainerProgress_APPLYING_LAYER {
		t.Fatalf("phase = %v; want APPLYING_LAYER", got.GetPhase())
	}
	if got.GetLayerIndex() != 2 {
		t.Fatalf("layer index = %d; want 2", got.GetLayerIndex())
	}
	if got.GetTotalLayers() != 5 {
		t.Fatalf("total layers = %d; want 5", got.GetTotalLayers())
	}
	if got.GetLayerSize() != 1234 {
		t.Fatalf("layer size = %d; want 1234", got.GetLayerSize())
	}
	if !got.GetReusedSnapshot() {
		t.Fatal("expected reused snapshot to be true")
	}
}

func TestCreateContainerProgressMappingUsesUnpackingPhaseForStart(t *testing.T) {
	progress := UnpackProgress{
		Phase:       "start",
		TotalLayers: 3,
	}

	got := toCreateContainerProgress(progress)

	if got.GetPhase() != agentpb.CreateContainerProgress_UNPACKING {
		t.Fatalf("phase = %v; want UNPACKING", got.GetPhase())
	}
	if got.GetTotalLayers() != 3 {
		t.Fatalf("total layers = %d; want 3", got.GetTotalLayers())
	}
	if got.GetLayerIndex() != 0 {
		t.Fatalf("layer index = %d; want 0", got.GetLayerIndex())
	}
}

func TestCreateContainerProgressMappingUsesUnpackingPhaseForLayerStart(t *testing.T) {
	progress := UnpackProgress{
		Phase:       "layer-start",
		LayerIndex:  1,
		TotalLayers: 4,
		LayerSize:   2048,
	}

	got := toCreateContainerProgress(progress)

	if got.GetPhase() != agentpb.CreateContainerProgress_UNPACKING {
		t.Fatalf("phase = %v; want UNPACKING", got.GetPhase())
	}
	if got.GetLayerIndex() != 1 {
		t.Fatalf("layer index = %d; want 1", got.GetLayerIndex())
	}
	if got.GetTotalLayers() != 4 {
		t.Fatalf("total layers = %d; want 4", got.GetTotalLayers())
	}
	if got.GetLayerSize() != 2048 {
		t.Fatalf("layer size = %d; want 2048", got.GetLayerSize())
	}
}

func TestBuildContainerBaseEnvIncludesWendyHostname(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "wendyos-test-device.local" }

	env, err := buildContainerBaseEnv("demo-app", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "WENDY_HOSTNAME=wendyos-test-device.local"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("env missing %q; got %v", want, env)
}

func TestBuildContainerBaseEnvOmitsWendyHostnameWhenUnavailable(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "" }

	env, err := buildContainerBaseEnv("demo-app", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, kv := range env {
		if len(kv) >= len("WENDY_HOSTNAME=") && kv[:len("WENDY_HOSTNAME=")] == "WENDY_HOSTNAME=" {
			t.Errorf("env unexpectedly contains %q when device hostname is unresolvable", kv)
		}
	}
}

func TestBuildContainerBaseEnvIncludesAppID(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "" }

	env, err := buildContainerBaseEnv("demo-app", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "WENDY_APP_ID=demo-app"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("env missing %q; got %v", want, env)
}

func TestBuildContainerBaseEnvOmitsAppIDWhenEmpty(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "" }

	env, err := buildContainerBaseEnv("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, kv := range env {
		if strings.HasPrefix(kv, "WENDY_APP_ID=") {
			t.Errorf("env unexpectedly contains %q when appID is empty", kv)
		}
	}
}

func TestBuildContainerBaseEnvMultiServiceHostname(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "wendyos-test-device.local" }

	env, err := buildContainerBaseEnv("com.example.app", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantHostname := "WENDY_HOSTNAME=api.local"
	wantDeviceHostname := "WENDY_DEVICE_HOSTNAME=wendyos-test-device.local"
	wantGroup := "WENDY_APP_GROUP=com.example.app"
	wantAppID := "WENDY_APP_ID=com.example.app"
	foundHostname, foundDeviceHostname, foundGroup, foundAppID := false, false, false, false
	for _, kv := range env {
		switch kv {
		case wantHostname:
			foundHostname = true
		case wantDeviceHostname:
			foundDeviceHostname = true
		case wantGroup:
			foundGroup = true
		case wantAppID:
			foundAppID = true
		}
	}
	if !foundHostname {
		t.Errorf("env missing %q; got %v", wantHostname, env)
	}
	if !foundDeviceHostname {
		t.Errorf("env missing %q; got %v", wantDeviceHostname, env)
	}
	if !foundGroup {
		t.Errorf("env missing %q; got %v", wantGroup, env)
	}
	if !foundAppID {
		t.Errorf("env missing %q; got %v", wantAppID, env)
	}
}

func TestBuildContainerBaseEnvMultiServiceNoDeviceHostname(t *testing.T) {
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return "device.local" }

	// For multi-service containers the device hostname must not appear.
	env, err := buildContainerBaseEnv("com.example.app", "worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, kv := range env {
		if kv == "WENDY_HOSTNAME=device.local" {
			t.Errorf("multi-service env must not use device hostname; got %v", env)
		}
	}
}

// TestBuildContainerBaseEnvIdentityVars documents the full env-var contract
// for WENDY_HOSTNAME, WENDY_DEVICE_HOSTNAME, WENDY_APP_GROUP, and WENDY_APP_ID
// across both app types.
//
// Single-container app  (serviceName == ""):
//
//	WENDY_HOSTNAME        = device hostname (e.g. "wendyos-abc.local")
//	WENDY_DEVICE_HOSTNAME = device hostname (same as WENDY_HOSTNAME)
//	WENDY_APP_ID          = appID
//	(WENDY_APP_GROUP is not set)
//
// Multi-service app (serviceName != ""):
//
//	WENDY_HOSTNAME        = "{serviceName}.local"   ← distinct per service
//	WENDY_DEVICE_HOSTNAME = device hostname          ← always the host mDNS name
//	WENDY_APP_GROUP       = appID                   ← lets a service discover siblings
//	WENDY_APP_ID          = appID
func TestBuildContainerBaseEnvIdentityVars(t *testing.T) {
	const deviceHost = "wendyos-abc.local"
	old := deviceHostnameWithSuffix
	t.Cleanup(func() { deviceHostnameWithSuffix = old })
	deviceHostnameWithSuffix = func() string { return deviceHost }

	tests := []struct {
		name               string
		appID              string
		serviceName        string
		wantHostname       string
		wantDeviceHostname string
		wantGroup          string // "" means the var must NOT be present
		wantAppID          string
	}{
		{
			name:               "single-container: hostname is device, no app group",
			appID:              "com.example.myapp",
			serviceName:        "",
			wantHostname:       deviceHost,
			wantDeviceHostname: deviceHost,
			wantGroup:          "",
			wantAppID:          "com.example.myapp",
		},
		{
			name:               "multi-service api: hostname is serviceName.local, device hostname still set",
			appID:              "com.example.myapp",
			serviceName:        "api",
			wantHostname:       "api.local",
			wantDeviceHostname: deviceHost,
			wantGroup:          "com.example.myapp",
			wantAppID:          "com.example.myapp",
		},
		{
			name:               "multi-service worker: hostname is serviceName.local, device hostname still set",
			appID:              "com.example.myapp",
			serviceName:        "worker",
			wantHostname:       "worker.local",
			wantDeviceHostname: deviceHost,
			wantGroup:          "com.example.myapp",
			wantAppID:          "com.example.myapp",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env, err := buildContainerBaseEnv(tc.appID, tc.serviceName)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			envMap := make(map[string]string)
			for _, kv := range env {
				if i := strings.Index(kv, "="); i >= 0 {
					envMap[kv[:i]] = kv[i+1:]
				}
			}

			if got := envMap["WENDY_HOSTNAME"]; got != tc.wantHostname {
				t.Errorf("WENDY_HOSTNAME = %q, want %q", got, tc.wantHostname)
			}
			if got := envMap["WENDY_DEVICE_HOSTNAME"]; got != tc.wantDeviceHostname {
				t.Errorf("WENDY_DEVICE_HOSTNAME = %q, want %q", got, tc.wantDeviceHostname)
			}
			if tc.wantGroup == "" {
				if _, ok := envMap["WENDY_APP_GROUP"]; ok {
					t.Errorf("WENDY_APP_GROUP must not be set for single-container apps; got %q", envMap["WENDY_APP_GROUP"])
				}
			} else {
				if got := envMap["WENDY_APP_GROUP"]; got != tc.wantGroup {
					t.Errorf("WENDY_APP_GROUP = %q, want %q", got, tc.wantGroup)
				}
			}
			if got := envMap["WENDY_APP_ID"]; got != tc.wantAppID {
				t.Errorf("WENDY_APP_ID = %q, want %q", got, tc.wantAppID)
			}
		})
	}
}

func hostNetworkCfg() *appconfig.AppConfig {
	return &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork, Mode: "host"},
		},
	}
}

func TestInjectOTELEnvDefaultPort(t *testing.T) {
	t.Setenv("WENDY_OTEL_PORT", "")

	env := injectOTELEnvIfNeeded(nil, hostNetworkCfg(), "")

	want := "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("env missing %q; got %v", want, env)
}

func TestInjectOTELEnvCustomPort(t *testing.T) {
	t.Setenv("WENDY_OTEL_PORT", "9999")

	env := injectOTELEnvIfNeeded(nil, hostNetworkCfg(), "")

	want := "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:9999"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("env missing %q; got %v", want, env)
}

func TestInjectOTELEnvSetsGRPCProtocol(t *testing.T) {
	env := injectOTELEnvIfNeeded(nil, hostNetworkCfg(), "")

	const want = "OTEL_EXPORTER_OTLP_PROTOCOL=grpc"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("env missing %q; got %v", want, env)
}

func TestInjectOTELEnvSkipsWithoutHostNetworking(t *testing.T) {
	cfg := &appconfig.AppConfig{} // no network entitlement

	env := injectOTELEnvIfNeeded(nil, cfg, "")

	for _, kv := range env {
		if len(kv) > len("OTEL_EXPORTER_OTLP_ENDPOINT=") &&
			kv[:len("OTEL_EXPORTER_OTLP_ENDPOINT=")] == "OTEL_EXPORTER_OTLP_ENDPOINT=" {
			t.Errorf("unexpected OTEL var injected without host networking: %q", kv)
		}
	}
}

func TestInjectOTELEnvSkipsWhenEndpointAlreadySet(t *testing.T) {
	existing := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://custom-collector:4317"}

	env := injectOTELEnvIfNeeded(existing, hostNetworkCfg(), "")

	count := 0
	for _, kv := range env {
		if len(kv) > len("OTEL_EXPORTER_OTLP_ENDPOINT=") &&
			kv[:len("OTEL_EXPORTER_OTLP_ENDPOINT=")] == "OTEL_EXPORTER_OTLP_ENDPOINT=" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 OTEL_EXPORTER_OTLP_ENDPOINT entry, got %d: %v", count, env)
	}
}

func TestInjectOTELEnvDoesNotOverrideExistingProtocol(t *testing.T) {
	existing := []string{"OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"}

	env := injectOTELEnvIfNeeded(existing, hostNetworkCfg(), "")

	count := 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_PROTOCOL=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 OTEL_EXPORTER_OTLP_PROTOCOL entry, got %d: %v", count, env)
	}
	for _, kv := range env {
		if kv == "OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf" {
			return
		}
	}
	t.Errorf("image-set protocol was overridden; got %v", env)
}

func TestInjectOTELEnvInvalidPortFallsBackToDefault(t *testing.T) {
	t.Setenv("WENDY_OTEL_PORT", "notaport")

	env := injectOTELEnvIfNeeded(nil, hostNetworkCfg(), "")

	const want = "OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:4317"
	for _, kv := range env {
		if kv == want {
			return
		}
	}
	t.Errorf("expected fallback to default port; got %v", env)
}

func hostNetworkCfgWithID(appID string) *appconfig.AppConfig {
	cfg := hostNetworkCfg()
	cfg.AppID = appID
	return cfg
}

func TestInjectOTELEnvSetsServiceNameAndResourceAttrs(t *testing.T) {
	env := injectOTELEnvIfNeeded(nil, hostNetworkCfgWithID("my-app"), "my-app")

	wantService := false
	wantAttrs := false
	for _, kv := range env {
		switch kv {
		case "OTEL_SERVICE_NAME=my-app":
			wantService = true
		case "OTEL_RESOURCE_ATTRIBUTES=wendy.app.name=my-app":
			wantAttrs = true
		}
	}
	if !wantService {
		t.Errorf("env missing OTEL_SERVICE_NAME=my-app; got %v", env)
	}
	if !wantAttrs {
		t.Errorf("env missing OTEL_RESOURCE_ATTRIBUTES=wendy.app.name=my-app; got %v", env)
	}
}

func TestInjectOTELEnvSetsIdentityWhenEndpointPreset(t *testing.T) {
	// An image that presets only the endpoint should still get identity vars so
	// its direct OTLP logs remain filterable by `wendy device logs --app <id>`.
	existing := []string{"OTEL_EXPORTER_OTLP_ENDPOINT=http://custom-collector:4317"}

	env := injectOTELEnvIfNeeded(existing, hostNetworkCfgWithID("my-app"), "my-app")

	endpointCount, wantService, wantAttrs := 0, false, false
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_ENDPOINT="):
			endpointCount++
		case kv == "OTEL_SERVICE_NAME=my-app":
			wantService = true
		case kv == "OTEL_RESOURCE_ATTRIBUTES=wendy.app.name=my-app":
			wantAttrs = true
		}
	}
	if endpointCount != 1 {
		t.Errorf("expected image endpoint preserved (1 entry), got %d: %v", endpointCount, env)
	}
	if !wantService || !wantAttrs {
		t.Errorf("expected identity vars injected alongside preset endpoint; got %v", env)
	}
}

func TestInjectOTELEnvOmitsServiceNameWhenAppIDEmpty(t *testing.T) {
	env := injectOTELEnvIfNeeded(nil, hostNetworkCfgWithID(""), "")

	for _, kv := range env {
		if strings.HasPrefix(kv, "OTEL_SERVICE_NAME=") ||
			strings.HasPrefix(kv, "OTEL_RESOURCE_ATTRIBUTES=") {
			t.Errorf("env unexpectedly contains %q when appID is empty", kv)
		}
	}
}

func TestInjectOTELEnvDoesNotOverrideExistingServiceName(t *testing.T) {
	existing := []string{
		"OTEL_SERVICE_NAME=custom",
		"OTEL_RESOURCE_ATTRIBUTES=deployment.environment=prod",
	}

	env := injectOTELEnvIfNeeded(existing, hostNetworkCfgWithID("my-app"), "my-app")

	serviceCount, attrCount := 0, 0
	for _, kv := range env {
		if strings.HasPrefix(kv, "OTEL_SERVICE_NAME=") {
			serviceCount++
		}
		if strings.HasPrefix(kv, "OTEL_RESOURCE_ATTRIBUTES=") {
			attrCount++
		}
	}
	if serviceCount != 1 || attrCount != 1 {
		t.Errorf("expected image-set OTEL_SERVICE_NAME/OTEL_RESOURCE_ATTRIBUTES to be preserved; got %v", env)
	}
	for _, kv := range env {
		if kv == "OTEL_SERVICE_NAME=my-app" || kv == "OTEL_RESOURCE_ATTRIBUTES=wendy.app.name=my-app" {
			t.Errorf("image-set OTel resource values were overridden; got %v", env)
		}
	}
}

func TestHasHostNetworkEntitlementEmptyModeIsHost(t *testing.T) {
	cfg := &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork, Mode: ""},
		},
	}
	if !hasHostNetworkEntitlement(cfg) {
		t.Error("empty mode should imply host networking")
	}
}

// TestHasHostNetworkEntitlementHostAdmin verifies the WDY-1094 opt-in mode is
// still recognised as host networking (it shares the host network stack; it
// just additionally carries CAP_NET_ADMIN), so host-network-dependent wiring
// like OTEL endpoint injection continues to apply.
func TestHasHostNetworkEntitlementHostAdmin(t *testing.T) {
	cfg := &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementNetwork, Mode: "host-admin"},
		},
	}
	if !hasHostNetworkEntitlement(cfg) {
		t.Error("host-admin mode should imply host networking")
	}
}

func TestExpandAgentHook(t *testing.T) {
	t.Setenv("EXTRA_VALUE", "ok")

	got := expandAgentHook("echo ${WENDY_APP_ID} ${WENDY_HOSTNAME} ${EXTRA_VALUE}", "camera-app")
	want := "echo camera-app localhost ok"
	if got != want {
		t.Fatalf("expandAgentHook = %q; want %q", got, want)
	}
}

func TestExpandAgentHookMissingEnv(t *testing.T) {
	t.Setenv("MISSING_VALUE", "")

	got := expandAgentHook("echo ${MISSING_VALUE}", "app")
	if got != "echo " {
		t.Fatalf("expandAgentHook missing env = %q; want empty expansion", got)
	}
}

// TestWrapWithDebugpyBindsLoopback is the regression test for WDY-1010: the
// debugpy DAP listener must bind to loopback (127.0.0.1), never 0.0.0.0.
// Binding all interfaces exposed unauthenticated Python RCE to anyone on the
// device's network for the duration of a debug session. Remote attach now goes
// through a device-side tunnel to loopback.
func TestWrapWithDebugpyBindsLoopback(t *testing.T) {
	cases := map[string][]string{
		"python entrypoint":     {"python3", "app.py"},
		"non-python entrypoint": {"/app/server"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			got := wrapWithDebugpy(args)
			joined := strings.Join(got, " ")
			if strings.Contains(joined, "0.0.0.0:5678") {
				t.Fatalf("debugpy must not bind 0.0.0.0; got %q", joined)
			}
			if !slices.Contains(got, "127.0.0.1:5678") {
				t.Fatalf("debugpy must bind loopback 127.0.0.1:5678; got %q", joined)
			}
		})
	}
}

func TestStartPostStartAgentHookSkippedWhenEmpty(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	var calls int
	startPostStartHookCommand = func(_ []string) (func() error, error) {
		calls++
		return func() error { return nil }, nil
	}

	client := &Client{logger: zap.NewNop()}
	started := client.startPostStartAgentHook("", "camera-app")
	if started {
		t.Fatal("startPostStartAgentHook returned true without command")
	}
	if calls != 0 {
		t.Fatalf("hook runner called %d times; want 0", calls)
	}
}

func TestStartPostStartAgentHookRunsWhenPresent(t *testing.T) {
	t.Setenv("EXTRA_VALUE", "ok")
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	var gotArgv []string
	startPostStartHookCommand = func(argv []string) (func() error, error) {
		gotArgv = argv
		return func() error { return nil }, nil
	}

	client := &Client{logger: zap.NewNop()}
	started := client.startPostStartAgentHook("echo ${WENDY_APP_ID} ${WENDY_HOSTNAME} ${EXTRA_VALUE}", "camera-app")
	if !started {
		t.Fatal("startPostStartAgentHook returned false with command")
	}
	wantArgv := []string{"echo", "camera-app", "localhost", "ok"}
	if !slices.Equal(gotArgv, wantArgv) {
		t.Fatalf("hook argv = %q; want %q", gotArgv, wantArgv)
	}
}

// TestStartPostStartAgentHookDoesNotInterpretShellMetacharacters is the
// regression test for WDY-1009: the hook command must be executed directly via
// argv, never handed to a shell. Shell metacharacters (;, &&, |, $(...)) must
// survive as inert literal arguments to argv[0] rather than spawning new
// commands.
func TestStartPostStartAgentHookDoesNotInterpretShellMetacharacters(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	var gotArgv []string
	startPostStartHookCommand = func(argv []string) (func() error, error) {
		gotArgv = argv
		return func() error { return nil }, nil
	}

	client := &Client{logger: zap.NewNop()}
	started := client.startPostStartAgentHook("/app/post-start.sh ; touch /tmp/pwned && rm -rf /", "camera-app")
	if !started {
		t.Fatal("startPostStartAgentHook returned false with command")
	}
	if len(gotArgv) == 0 {
		t.Fatal("hook argv is empty")
	}
	// The program executed is the first token only; the injected command tokens
	// must appear verbatim as arguments, proving no shell parsed them.
	for _, tok := range gotArgv {
		if tok == "sh" || tok == "-c" || tok == "cmd.exe" || tok == "/C" {
			t.Fatalf("hook argv must not invoke a shell, got %q", gotArgv)
		}
	}
	// The whole command must survive as argv[0] (the program) plus inert literal
	// tokens — the metacharacters are arguments, never separators.
	wantArgv := []string{"/app/post-start.sh", ";", "touch", "/tmp/pwned", "&&", "rm", "-rf", "/"}
	if !slices.Equal(gotArgv, wantArgv) {
		t.Fatalf("hook argv = %q; want %q", gotArgv, wantArgv)
	}
}

// TestStartPostStartAgentHookEmptyExpansionLogsConfiguredCommand asserts the
// "expanded to empty" warning is actionable: it carries the raw, pre-expansion
// command from wendy.json so an operator can tell which hook misfired. The raw
// command holds variable references (not their expanded values), so it is safe
// to log — unlike the expanded string, which may contain secrets.
func TestStartPostStartAgentHookEmptyExpansionLogsConfiguredCommand(t *testing.T) {
	t.Setenv("MISSING_VALUE", "")
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })
	startPostStartHookCommand = func(_ []string) (func() error, error) {
		return func() error { return nil }, nil
	}

	core, observed := observer.New(zap.WarnLevel)
	client := &Client{logger: zap.New(core)}
	client.startPostStartAgentHook("${MISSING_VALUE}", "camera-app")

	logs := observed.FilterMessageSnippet("empty command")
	if logs.Len() != 1 {
		t.Fatalf("expected one empty-command warning; got %d", logs.Len())
	}
	var found bool
	for _, f := range logs.All()[0].Context {
		if f.Key == "configured_command" && f.String == "${MISSING_VALUE}" {
			found = true
		}
	}
	if !found {
		t.Fatalf("empty-command warning must include the raw configured_command field; got %+v", logs.All()[0].Context)
	}
}

// TestStartPostStartAgentHookWarnsOnQuotedArguments guards the silent
// behavioral regression from dropping `sh -c`: strings.Fields does not honor
// quotes, so a quoted argument is split on whitespace. The hook still runs
// (best-effort), but the operator must get a warning rather than silent
// mis-execution.
func TestStartPostStartAgentHookWarnsOnQuotedArguments(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })
	var calls int
	startPostStartHookCommand = func(_ []string) (func() error, error) {
		calls++
		return func() error { return nil }, nil
	}

	core, observed := observer.New(zap.WarnLevel)
	client := &Client{logger: zap.New(core)}
	started := client.startPostStartAgentHook(`/app/run --message "hello world"`, "camera-app")

	if !started {
		t.Fatal("hook with quoted args should still run (best-effort)")
	}
	if calls != 1 {
		t.Fatalf("hook runner called %d times; want 1", calls)
	}
	if observed.FilterMessageSnippet("quot").Len() == 0 {
		t.Fatal("expected a warning that quoting is not honored")
	}
}

// TestStartPostStartHookCommandRejectsEmptyArgv keeps the argv[0] indexing
// invariant local to the runner: an empty argv must return an error, never
// panic, even if a future caller forgets the length guard.
func TestStartPostStartHookCommandRejectsEmptyArgv(t *testing.T) {
	if _, err := startPostStartHookCommand(nil); err == nil {
		t.Fatal("expected error for empty argv, got nil")
	}
}

// TestStartPostStartAgentHookSkippedWhenExpansionEmpty guards the case where a
// command consists solely of an env reference that expands to nothing: there is
// no program to run, so the runner must not be invoked.
func TestStartPostStartAgentHookSkippedWhenExpansionEmpty(t *testing.T) {
	t.Setenv("MISSING_VALUE", "")
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	var calls int
	startPostStartHookCommand = func(_ []string) (func() error, error) {
		calls++
		return func() error { return nil }, nil
	}

	client := &Client{logger: zap.NewNop()}
	started := client.startPostStartAgentHook("${MISSING_VALUE}", "camera-app")
	if started {
		t.Fatal("startPostStartAgentHook returned true for a command that expanded to nothing")
	}
	if calls != 0 {
		t.Fatalf("hook runner called %d times; want 0", calls)
	}
}

func TestStartPostStartAgentHookStartErrorDoesNotLogCommand(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	startPostStartHookCommand = func(_ []string) (func() error, error) {
		return nil, errors.New("start failed")
	}

	core, observed := observer.New(zap.WarnLevel)
	client := &Client{logger: zap.New(core)}
	started := client.startPostStartAgentHook("echo secret-token-value", "camera-app")
	if started {
		t.Fatal("startPostStartAgentHook returned true after start error")
	}

	logs := observed.FilterMessage("Failed to start postStart agent hook")
	if logs.Len() != 1 {
		t.Fatalf("warning log count = %d; want 1", logs.Len())
	}
	if observed.FilterMessageSnippet("secret-token-value").Len() != 0 {
		t.Fatal("hook command leaked into warning message")
	}
	for _, field := range logs.All()[0].Context {
		if field.Key == "command" {
			t.Fatal("hook command leaked into warning fields")
		}
	}
}

func TestLayerMediaType_Zstd(t *testing.T) {
	got := layerMediaType(agentpb.RunContainerLayerHeader_COMPRESSION_ZSTD, false)
	want := "application/vnd.oci.image.layer.v1.tar+zstd"
	if got != want {
		t.Errorf("layerMediaType(ZSTD, false) = %q; want %q", got, want)
	}
}

func TestLayerMediaType_ZstdIgnoresGzipBool(t *testing.T) {
	got := layerMediaType(agentpb.RunContainerLayerHeader_COMPRESSION_ZSTD, true)
	want := "application/vnd.oci.image.layer.v1.tar+zstd"
	if got != want {
		t.Errorf("layerMediaType(ZSTD, true) = %q; want %q", got, want)
	}
}

func TestLayerMediaType_None(t *testing.T) {
	got := layerMediaType(agentpb.RunContainerLayerHeader_COMPRESSION_NONE, true)
	want := "application/vnd.oci.image.layer.v1.tar"
	if got != want {
		t.Errorf("layerMediaType(NONE, true) = %q; want %q", got, want)
	}
}

func TestLayerMediaType_GzipDefault_GzipTrue(t *testing.T) {
	// Old CLI path: compression field absent (zero value = GZIP), gzip=true.
	got := layerMediaType(agentpb.RunContainerLayerHeader_COMPRESSION_GZIP, true)
	want := "application/vnd.oci.image.layer.v1.tar+gzip"
	if got != want {
		t.Errorf("layerMediaType(GZIP, true) = %q; want %q", got, want)
	}
}

func TestLayerMediaType_GzipDefault_GzipFalse(t *testing.T) {
	// Old CLI path: compression field absent (zero value = GZIP), gzip=false → uncompressed.
	got := layerMediaType(agentpb.RunContainerLayerHeader_COMPRESSION_GZIP, false)
	want := "application/vnd.oci.image.layer.v1.tar"
	if got != want {
		t.Errorf("layerMediaType(GZIP, false) = %q; want %q", got, want)
	}
}

func TestResolveStopOrder_ReversesTopoOrder(t *testing.T) {
	services := map[string]*appconfig.ServiceConfig{
		"db":  {},
		"api": {DependsOn: []string{"db"}},
	}
	order, err := appconfig.ServiceTopoOrder(services)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Reverse for stop order: dependents first, then dependencies.
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	if len(order) != 2 || order[0] != "api" || order[1] != "db" {
		t.Errorf("expected [api db], got %v", order)
	}
}

func TestPrimaryPIDTracking(t *testing.T) {
	c := &Client{primaryPIDs: make(map[string]uint32)}
	c.setPrimaryPID("com.example.app", 12345)
	got, ok := c.getPrimaryPID("com.example.app")
	if !ok {
		t.Fatal("expected primary PID to be found")
	}
	if got != 12345 {
		t.Fatalf("got PID %d, want 12345", got)
	}
	c.clearPrimaryPID("com.example.app")
	_, ok = c.getPrimaryPID("com.example.app")
	if ok {
		t.Fatal("expected primary PID to be cleared")
	}
}

func envContains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return e[len(prefix):], true
		}
	}
	return "", false
}

func TestBuildROS2Env_WithConfig(t *testing.T) {
	domainID := 42
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{
				DomainID: &domainID,
				RMW:      "rmw_cyclonedds_cpp",
			},
		},
	}
	got := buildROS2Env(cfg, "com.example.app", "")
	for _, want := range []string{
		"ROS_DOMAIN_ID=42",
		"RMW_IMPLEMENTATION=rmw_cyclonedds_cpp",
		"ROS_LOCALHOST_ONLY=1",
	} {
		if !envContains(got, want) {
			t.Errorf("expected %s in env, got %v", want, got)
		}
	}
	if uri, ok := envValue(got, "CYCLONEDDS_URI"); !ok || !strings.Contains(uri, "<SharedMemory>") {
		t.Errorf("expected inline CYCLONEDDS_URI with shared memory config, got %v", got)
	}
}

func TestBuildROS2Env_NoConfig(t *testing.T) {
	cfg := &appconfig.AppConfig{}
	got := buildROS2Env(cfg, "com.example.app", "")
	if len(got) != 0 {
		t.Errorf("expected empty env for no ROS2 config, got %v", got)
	}
}

func TestBuildROS2Env_AutoDomainID(t *testing.T) {
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{}},
	}
	got := buildROS2Env(cfg, "com.example.app", "")
	val, ok := envValue(got, "ROS_DOMAIN_ID")
	if !ok {
		t.Fatalf("expected ROS_DOMAIN_ID in env, got %v", got)
	}
	id, err := strconv.Atoi(val)
	if err != nil || id < 0 || id > 232 {
		t.Errorf("auto domain ID = %q, want integer in [0,232]", val)
	}
	// Stable: a second call for the same appId must produce the same ID.
	again := buildROS2Env(cfg, "com.example.app", "")
	if val2, _ := envValue(again, "ROS_DOMAIN_ID"); val2 != val {
		t.Errorf("auto domain ID not stable: %q vs %q", val, val2)
	}
	// Defaults: CycloneDDS RMW with inline config.
	if !envContains(got, "RMW_IMPLEMENTATION=rmw_cyclonedds_cpp") {
		t.Errorf("expected default RMW_IMPLEMENTATION=rmw_cyclonedds_cpp, got %v", got)
	}
}

func TestBuildROS2Env_InvalidDomainID(t *testing.T) {
	domainID := 500
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{DomainID: &domainID},
		},
	}
	if got := buildROS2Env(cfg, "com.example.app", ""); len(got) != 0 {
		t.Errorf("expected empty env for out-of-range domain ID, got %v", got)
	}
}

func TestBuildROS2Env_NonCycloneRMWSkipsCycloneURI(t *testing.T) {
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{RMW: "fastrtps"},
		},
	}
	got := buildROS2Env(cfg, "com.example.app", "")
	if !envContains(got, "RMW_IMPLEMENTATION=rmw_fastrtps_cpp") {
		t.Errorf("expected short rmw name to normalize to rmw_fastrtps_cpp, got %v", got)
	}
	if _, ok := envValue(got, "CYCLONEDDS_URI"); ok {
		t.Errorf("CYCLONEDDS_URI must not be injected for non-CycloneDDS RMW, got %v", got)
	}
}

func TestBuildROS2Env_ServiceOverride(t *testing.T) {
	groupID, svcID := 1, 7
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{DomainID: &groupID}},
		Services: map[string]*appconfig.ServiceConfig{
			"detector": {
				Frameworks: &appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{DomainID: &svcID}},
			},
			"camera": {},
		},
	}
	got := buildROS2Env(cfg, "com.example.app", "detector")
	if !envContains(got, "ROS_DOMAIN_ID=7") {
		t.Errorf("expected service-level override ROS_DOMAIN_ID=7, got %v", got)
	}
	got = buildROS2Env(cfg, "com.example.app", "camera")
	if !envContains(got, "ROS_DOMAIN_ID=1") {
		t.Errorf("expected group-level ROS_DOMAIN_ID=1 for camera, got %v", got)
	}
}

// TestSharedSHMPath verifies the shared-ipc shm path is derived from a valid
// app ID and that an invalid app ID is rejected (the path is later stat'd to
// detect shared-ipc topology and bind-mounted into the ROS 2 sidecar, WDY-1555).
func TestSharedSHMPath(t *testing.T) {
	path, err := sharedSHMPath("sh.wendy.examples.so101")
	if err != nil {
		t.Fatalf("sharedSHMPath valid app ID: unexpected error %v", err)
	}
	if want := "/run/wendy/shm/sh.wendy.examples.so101"; path != want {
		t.Errorf("sharedSHMPath = %q, want %q", path, want)
	}
	if _, err := sharedSHMPath(""); err == nil {
		t.Error("sharedSHMPath(\"\") = nil error, want validation error")
	}
	if _, err := sharedSHMPath("../escape"); err == nil {
		t.Error("sharedSHMPath(\"../escape\") = nil error, want validation error")
	}
}

// TestRMWFromEnv verifies the ROS 2 sidecar reads back the anchor app's
// RMW_IMPLEMENTATION from its OCI spec env so it can match the app's DDS
// implementation (WDY-1593).
func TestRMWFromEnv(t *testing.T) {
	got := rmwFromEnv([]string{"ROS_DOMAIN_ID=42", "RMW_IMPLEMENTATION=rmw_cyclonedds_cpp", "ROS_LOCALHOST_ONLY=1"})
	if got != "rmw_cyclonedds_cpp" {
		t.Errorf("rmwFromEnv = %q, want rmw_cyclonedds_cpp", got)
	}
	if got := rmwFromEnv([]string{"ROS_DOMAIN_ID=42"}); got != "" {
		t.Errorf("rmwFromEnv (absent) = %q, want empty", got)
	}
	if got := rmwFromEnv(nil); got != "" {
		t.Errorf("rmwFromEnv(nil) = %q, want empty", got)
	}
}
