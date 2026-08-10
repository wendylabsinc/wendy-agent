package containerd

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// mustBuildROS2Env is the happy-path helper for the existing tests, which were
// written against buildROS2Env's old single-return signature. It now returns an
// error so an unresolvable config fails the container create instead of silently
// starting it with no ROS 2 environment (see TestBuildROS2Env_InvalidDomainID).
func mustBuildROS2Env(t *testing.T, cfg *appconfig.AppConfig, appID, serviceName string) []string {
	t.Helper()
	env, err := buildROS2Env(cfg, appID, serviceName)
	if err != nil {
		t.Fatalf("buildROS2Env(%q, %q): %v", appID, serviceName, err)
	}
	return env
}

// App-local isolation used to rest entirely on ROS_LOCALHOST_ONLY, which has been
// deprecated since ROS 2 Iron in favour of ROS_AUTOMATIC_DISCOVERY_RANGE — while
// ROS2Config.Distro explicitly advertises "jazzy". On the newer distros we claim
// to support, the sole mechanism enforcing the documented "app" scope was at best
// deprecated; for an app carrying `network: host` it is the only thing between it
// and every other DDS participant on the device.

func TestBuildROS2Env_AppScopeSetsBothDiscoveryVariables(t *testing.T) {
	domainID := 42
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{DomainID: &domainID},
		},
	}
	env := mustBuildROS2Env(t, cfg, "com.example.app", "")
	for _, want := range []string{
		"ROS_LOCALHOST_ONLY=1",                    // pre-Iron
		"ROS_AUTOMATIC_DISCOVERY_RANGE=LOCALHOST", // Iron and later
	} {
		if !envContains(env, want) {
			t.Errorf("app scope must set %s, got %v", want, env)
		}
	}
}

func TestBuildROS2Env_HostScopeSetsBothDiscoveryVariables(t *testing.T) {
	domainID := 30
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{
				DomainID:       &domainID,
				DiscoveryScope: appconfig.ROS2DiscoveryScopeHost,
			},
		},
	}
	env := mustBuildROS2Env(t, cfg, "sh.wendy.foxglovebridge", "")
	for _, want := range []string{
		"ROS_LOCALHOST_ONLY=0",
		"ROS_AUTOMATIC_DISCOVERY_RANGE=SUBNET",
	} {
		if !envContains(env, want) {
			t.Errorf("host scope must set %s, got %v", want, env)
		}
	}
	if envContains(env, "ROS_AUTOMATIC_DISCOVERY_RANGE=LOCALHOST") {
		t.Errorf("host scope must not restrict discovery to localhost, got %v", env)
	}
}

func TestBuildROS2Env_DiscoveryVariablesAgreeWithEachOther(t *testing.T) {
	// The two variables must never disagree — a container that is localhost-only
	// under one and subnet-wide under the other is the worst of both worlds.
	for _, scope := range []string{appconfig.ROS2DiscoveryScopeApp, appconfig.ROS2DiscoveryScopeHost} {
		cfg := &appconfig.AppConfig{
			Frameworks: &appconfig.FrameworksConfig{
				ROS2: &appconfig.ROS2Config{DiscoveryScope: scope},
			},
		}
		env := mustBuildROS2Env(t, cfg, "com.example.app", "")
		localhostOnly, _ := envValue(env, "ROS_LOCALHOST_ONLY")
		discoveryRange, _ := envValue(env, "ROS_AUTOMATIC_DISCOVERY_RANGE")
		restrictedByOld := localhostOnly == "1"
		restrictedByNew := discoveryRange == "LOCALHOST"
		if restrictedByOld != restrictedByNew {
			t.Errorf("scope %q: ROS_LOCALHOST_ONLY=%q disagrees with ROS_AUTOMATIC_DISCOVERY_RANGE=%q",
				scope, localhostOnly, discoveryRange)
		}
	}
}

func TestBuildROS2Env_InvalidDiscoveryScopeIsAnError(t *testing.T) {
	// Same fail-closed reasoning as an out-of-range domain ID: returning an empty
	// env would start the container with no scope enforcement at all.
	cfg := &appconfig.AppConfig{
		Frameworks: &appconfig.FrameworksConfig{
			ROS2: &appconfig.ROS2Config{DiscoveryScope: "everywhere"},
		},
	}
	env, err := buildROS2Env(cfg, "com.example.app", "")
	if err == nil {
		t.Fatalf("expected an error for an unknown discoveryScope, got env %v", env)
	}
	if env != nil {
		t.Errorf("expected nil env alongside the error, got %v", env)
	}
	if !strings.Contains(err.Error(), "everywhere") {
		t.Errorf("error should quote the offending value, got: %v", err)
	}
}

func TestBuildROS2Env_NoROS2ConfigIsNotAnError(t *testing.T) {
	// A non-ROS 2 app must keep working: nil env, nil error.
	env, err := buildROS2Env(&appconfig.AppConfig{}, "com.example.app", "")
	if err != nil {
		t.Fatalf("a non-ROS 2 app must not error: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("expected no env for a non-ROS 2 app, got %v", env)
	}
}

// The auto domain ID is a 233-bucket FNV hash of the appId. It is a convenience,
// not an isolation boundary — with ~18 apps on a device the birthday bound gives
// a better-than-even chance two share a domain. Pin the range and the stability,
// and document the collision property so nobody mistakes it for isolation.
func TestROS2AutoDomainID_RangeAndStability(t *testing.T) {
	ids := map[int]string{}
	for _, appID := range []string{
		"com.example.a", "com.example.b", "com.example.c", "sh.wendy.foxglovebridge",
		"sh.wendy.examples.ros2", "org.robot.nav", "org.robot.perception",
	} {
		id := appconfig.ROS2AutoDomainID(appID)
		if id < appconfig.ROS2DomainIDMin || id > appconfig.ROS2DomainIDMax {
			t.Errorf("ROS2AutoDomainID(%q) = %d, outside [%d,%d]",
				appID, id, appconfig.ROS2DomainIDMin, appconfig.ROS2DomainIDMax)
		}
		if again := appconfig.ROS2AutoDomainID(appID); again != id {
			t.Errorf("ROS2AutoDomainID(%q) not stable: %d then %d", appID, id, again)
		}
		if prev, ok := ids[id]; ok {
			t.Logf("domain %d shared by %q and %q — expected: 233 buckets is a "+
				"convenience default, not an isolation boundary", id, prev, appID)
		}
		ids[id] = appID
	}
}
