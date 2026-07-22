package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// ros2ExampleAppConfig mirrors Examples/ROS2/wendy.json: group-level
// frameworks.ros2 + isolation, with a per-service override on one service.
func ros2ExampleAppConfig() *appconfig.AppConfig {
	groupDomain, svcDomain := 42, 7
	return &appconfig.AppConfig{
		AppID:     "sh.wendy.examples.ros2",
		Version:   "1.0.0",
		Platform:  "linux/arm64",
		Isolation: "shared-network",
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{DomainID: &groupDomain, RMW: "rmw_cyclonedds_cpp"},
		},
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementBluetooth}},
		Services: map[string]*appconfig.ServiceConfig{
			"talker": {Context: "./talker"},
			"listener": {
				Context:      "./listener",
				DependsOn:    []string{"talker"},
				Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementGPU}},
				Frameworks: &appconfig.FrameworksConfig{
					ROS2: &appconfig.ROS2Config{DomainID: &svcDomain},
				},
			},
		},
	}
}

// The per-service AppConfig transmitted to the agent must preserve the group
// identity and runtime context — dropping frameworks/isolation here was the
// root cause of ROS_DOMAIN_ID never reaching containers (WDY-884).
func TestMultiServiceCreateConfig_PreservesGroupContext(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	got := multiServiceCreateConfig(appCfg, "talker", appCfg.Services["talker"])

	if got.AppID != "sh.wendy.examples.ros2" {
		t.Errorf("AppID = %q, want unmangled group appId", got.AppID)
	}
	if got.ServiceName != "talker" {
		t.Errorf("ServiceName = %q, want talker", got.ServiceName)
	}
	if got.ContainerName() != "sh.wendy.examples.ros2_talker" {
		t.Errorf("ContainerName() = %q, want sh.wendy.examples.ros2_talker", got.ContainerName())
	}
	if got.Isolation != "shared-network" {
		t.Errorf("Isolation = %q, want shared-network", got.Isolation)
	}
	if got.Version != "1.0.0" || got.Platform != "linux/arm64" {
		t.Errorf("Version/Platform = %q/%q, want 1.0.0/linux/arm64", got.Version, got.Platform)
	}
	ros2 := got.GetROS2Config()
	if ros2 == nil || ros2.DomainID == nil || *ros2.DomainID != 42 {
		t.Fatalf("talker must inherit group frameworks.ros2 (domainId 42), got %+v", ros2)
	}
	// Group-level entitlements are shared with every service.
	if len(got.Entitlements) != 1 || got.Entitlements[0].Type != appconfig.EntitlementBluetooth {
		t.Errorf("talker entitlements = %+v, want shared bluetooth", got.Entitlements)
	}
}

func TestMultiServiceCreateConfig_ServiceFrameworksOverride(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	got := multiServiceCreateConfig(appCfg, "listener", appCfg.Services["listener"])

	ros2 := got.GetROS2Config()
	if ros2 == nil || ros2.DomainID == nil || *ros2.DomainID != 7 {
		t.Fatalf("listener must use its own frameworks.ros2 override (domainId 7), got %+v", ros2)
	}
	// Shared + per-service entitlements are merged.
	if len(got.Entitlements) != 2 {
		t.Errorf("listener entitlements = %+v, want shared bluetooth + gpu", got.Entitlements)
	}
}

// A service's own readiness/hooks must travel with its per-service AppConfig so
// startAndStreamServices can fire them scoped to that container (WDY-1271).
func TestMultiServiceCreateConfig_PropagatesReadinessHooks(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	readiness := &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 8080}, TimeoutSeconds: 5}
	hooks := &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:8080", Agent: "echo hi"}}
	svc := appCfg.Services["talker"]
	svc.Readiness = readiness
	svc.Hooks = hooks

	got := multiServiceCreateConfig(appCfg, "talker", svc)

	if got.Readiness != readiness {
		t.Errorf("Readiness = %p, want the same pointer as svc.Readiness (%p)", got.Readiness, readiness)
	}
	if got.Hooks != hooks {
		t.Errorf("Hooks = %p, want the same pointer as svc.Hooks (%p)", got.Hooks, hooks)
	}
}

// The group's top-level readiness/hooks are the app-level fallback, fired once
// after ALL services start — never per service. Copying them onto every
// per-service config would run hooks.postStart.agent in every container, so a
// service that declares nothing must get nil readiness/hooks (WDY-1271).
func TestMultiServiceCreateConfig_DoesNotInheritTopLevelHooks(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	appCfg.Readiness = &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 9090}}
	appCfg.Hooks = &appconfig.HooksConfig{PostStart: &appconfig.HookCommand{Agent: "echo app-level"}}

	got := multiServiceCreateConfig(appCfg, "talker", appCfg.Services["talker"])

	if got.Readiness != nil {
		t.Errorf("Readiness = %+v, want nil (top-level readiness must not leak per-service)", got.Readiness)
	}
	if got.Hooks != nil {
		t.Errorf("Hooks = %+v, want nil (top-level hooks must not leak per-service)", got.Hooks)
	}
}

func TestMultiServiceContainerName_MatchesAgentConvention(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	cfg := multiServiceCreateConfig(appCfg, "talker", appCfg.Services["talker"])
	// Start/stop in the multibuild path must address the same container name
	// the agent derives from (AppID, ServiceName) at creation time.
	if got := multiServiceContainerName(appCfg.AppID, "talker"); got != cfg.ContainerName() {
		t.Errorf("multiServiceContainerName = %q, ContainerName() = %q — start/stop would miss the container", got, cfg.ContainerName())
	}
}
