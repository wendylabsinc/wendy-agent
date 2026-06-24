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

func TestMultiServiceContainerName_MatchesAgentConvention(t *testing.T) {
	appCfg := ros2ExampleAppConfig()
	cfg := multiServiceCreateConfig(appCfg, "talker", appCfg.Services["talker"])
	// Start/stop in the multibuild path must address the same container name
	// the agent derives from (AppID, ServiceName) at creation time.
	if got := multiServiceContainerName(appCfg.AppID, "talker"); got != cfg.ContainerName() {
		t.Errorf("multiServiceContainerName = %q, ContainerName() = %q — start/stop would miss the container", got, cfg.ContainerName())
	}
}
