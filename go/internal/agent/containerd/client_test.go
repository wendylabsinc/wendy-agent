package containerd

import (
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

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

func TestStartPostStartAgentHookSkippedWhenEmpty(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	var calls int
	startPostStartHookCommand = func(_, _, _ string) (func() error, error) {
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

	var gotShell, gotFlag, gotCommand string
	startPostStartHookCommand = func(shell, flag, command string) (func() error, error) {
		gotShell = shell
		gotFlag = flag
		gotCommand = command
		return func() error { return nil }, nil
	}

	client := &Client{logger: zap.NewNop()}
	started := client.startPostStartAgentHook("echo ${WENDY_APP_ID} ${WENDY_HOSTNAME} ${EXTRA_VALUE}", "camera-app")
	if !started {
		t.Fatal("startPostStartAgentHook returned false with command")
	}
	if gotShell == "" || gotFlag == "" {
		t.Fatalf("shell command not populated: shell=%q flag=%q", gotShell, gotFlag)
	}
	wantCommand := "echo camera-app localhost ok"
	if gotCommand != wantCommand {
		t.Fatalf("hook command = %q; want %q", gotCommand, wantCommand)
	}
}

func TestStartPostStartAgentHookStartErrorDoesNotLogCommand(t *testing.T) {
	old := startPostStartHookCommand
	t.Cleanup(func() { startPostStartHookCommand = old })

	startPostStartHookCommand = func(_, _, _ string) (func() error, error) {
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

func TestBuildROS2Env_WithConfig(t *testing.T) {
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{
				DomainID: 42,
				RMW:      "rmw_cyclonedds_cpp",
			},
		},
	}
	got := buildROS2Env(cfg)
	found42 := false
	foundRMW := false
	for _, e := range got {
		if e == "ROS_DOMAIN_ID=42" {
			found42 = true
		}
		if e == "RMW_IMPLEMENTATION=rmw_cyclonedds_cpp" {
			foundRMW = true
		}
	}
	if !found42 {
		t.Errorf("expected ROS_DOMAIN_ID=42 in env, got %v", got)
	}
	if !foundRMW {
		t.Errorf("expected RMW_IMPLEMENTATION=rmw_cyclonedds_cpp in env, got %v", got)
	}
}

func TestBuildROS2Env_NoConfig(t *testing.T) {
	cfg := &appconfig.AppConfig{}
	got := buildROS2Env(cfg)
	if len(got) != 0 {
		t.Errorf("expected empty env for no ROS2 config, got %v", got)
	}
}
