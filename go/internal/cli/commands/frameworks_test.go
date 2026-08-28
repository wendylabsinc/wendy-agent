package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// chdirTo changes the working directory to dir for the duration of the test,
// restoring the original on cleanup. loadProjectConfig and saveProjectConfig
// resolve wendy.json relative to os.Getwd(), matching the pattern used by
// TestResolveRunWorkingDir_Default in commands_test.go. writeWendyJSON
// (camera_credentials_test.go) already builds the temp dir + file; this just
// makes it the working directory too.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prevWD)
	})
}

func TestNewFrameworksCmd_Wiring(t *testing.T) {
	cmd := newFrameworksCmd()
	if cmd.Use != "frameworks" {
		t.Errorf("Use = %q; want %q", cmd.Use, "frameworks")
	}
	for _, name := range []string{"list", "add", "remove"} {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q subcommand", name)
		}
	}
}

func TestConfiguredFrameworkTypes(t *testing.T) {
	if got := configuredFrameworkTypes(nil); got != nil {
		t.Errorf("nil frameworks = %v; want nil", got)
	}
	if got := configuredFrameworkTypes(&appconfig.FrameworksConfig{}); got != nil {
		t.Errorf("empty frameworks = %v; want nil", got)
	}
	got := configuredFrameworkTypes(&appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{}})
	if len(got) != 1 || got[0] != appconfig.FrameworkROS2 {
		t.Errorf("configuredFrameworkTypes() = %v; want [ros2]", got)
	}
}

// TestFrameworksAdd_UnknownType verifies `wendy project frameworks add
// <bad-type>` gives the same quality of feedback as
// `wendy project entitlements add <bad-type>`: the bad value plus the full
// list of valid ones, not a bare failure.
func TestFrameworksAdd_UnknownType(t *testing.T) {
	dir := writeWendyJSON(t, `{"appId": "com.example.robot"}`)
	chdirTo(t, dir)

	cmd := newFrameworksAddCmd()
	err := cmd.RunE(cmd, []string{"bogus"})
	if err == nil {
		t.Fatal("expected error for unknown framework type")
	}
	if !strings.Contains(err.Error(), `unknown framework type "bogus"`) {
		t.Errorf("error = %q; want it to name the bad type", err.Error())
	}
	if !strings.Contains(err.Error(), appconfig.FrameworkROS2) {
		t.Errorf("error = %q; want it to list valid types including %q", err.Error(), appconfig.FrameworkROS2)
	}
}

// TestFrameworksAdd_ROS2WritesDefaults verifies `add ros2` with no further
// input enables ROS 2 with all-defaults config, since every ROS2Config field
// already has a sensible default (unlike persist/i2c/gpio entitlements).
func TestFrameworksAdd_ROS2WritesDefaults(t *testing.T) {
	dir := writeWendyJSON(t, `{"appId": "com.example.robot"}`)
	chdirTo(t, dir)

	cmd := newFrameworksAddCmd()
	if err := cmd.RunE(cmd, []string{appconfig.FrameworkROS2}); err != nil {
		t.Fatalf("add ros2: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		t.Fatalf("reloading wendy.json: %v", err)
	}
	if cfg.Frameworks == nil || cfg.Frameworks.ROS2 == nil {
		t.Fatal("wendy.json was not updated with frameworks.ros2")
	}
}

func TestFrameworksAdd_AlreadyExists(t *testing.T) {
	dir := writeWendyJSON(t, `{
		"appId": "com.example.robot",
		"frameworks": {"ros2": {}}
	}`)
	chdirTo(t, dir)

	cmd := newFrameworksAddCmd()
	err := cmd.RunE(cmd, []string{appconfig.FrameworkROS2})
	if err == nil {
		t.Fatal("expected error re-adding an already-configured framework")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q; want it to say the framework already exists", err.Error())
	}
}

func TestFrameworksRemove(t *testing.T) {
	dir := writeWendyJSON(t, `{
		"appId": "com.example.robot",
		"frameworks": {"ros2": {"distro": "jazzy"}}
	}`)
	chdirTo(t, dir)

	cmd := newFrameworksRemoveCmd()
	if err := cmd.RunE(cmd, []string{appconfig.FrameworkROS2}); err != nil {
		t.Fatalf("remove ros2: %v", err)
	}

	cfg, err := appconfig.LoadFromFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		t.Fatalf("reloading wendy.json: %v", err)
	}
	if cfg.Frameworks != nil {
		t.Errorf("Frameworks = %+v; want nil after removing the only configured framework", cfg.Frameworks)
	}
}

func TestFrameworksRemove_NotFound(t *testing.T) {
	dir := writeWendyJSON(t, `{"appId": "com.example.robot"}`)
	chdirTo(t, dir)

	cmd := newFrameworksRemoveCmd()
	err := cmd.RunE(cmd, []string{appconfig.FrameworkROS2})
	if err == nil {
		t.Fatal("expected error removing a framework that isn't configured")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q; want it to say the framework was not found", err.Error())
	}
}

// TestEntitlementsAdd_FrameworkHint is the direct regression test for the
// friction this PR fixes: guessing `wendy project entitlements add ros2`
// used to fail with a generic "unknown entitlement type" error and no
// pointer to where ROS 2 actually lives. It should now name the real command.
func TestEntitlementsAdd_FrameworkHint(t *testing.T) {
	dir := writeWendyJSON(t, `{"appId": "com.example.robot"}`)
	chdirTo(t, dir)

	cmd := newEntitlementsAddCmd()
	err := cmd.RunE(cmd, []string{appconfig.FrameworkROS2})
	if err == nil {
		t.Fatal("expected error adding \"ros2\" as an entitlement")
	}
	if !strings.Contains(err.Error(), "wendy project frameworks add ros2") {
		t.Errorf("error = %q; want it to point at `wendy project frameworks add ros2`", err.Error())
	}
	if strings.Contains(err.Error(), "unknown entitlement type") {
		t.Errorf("error = %q; want the specific framework hint, not the generic unknown-type message", err.Error())
	}
}
