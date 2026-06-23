package containerd

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// TestCacheAppServicesLocked_MergeKeepsGroup covers WDY-1721: a single-service
// redeploy (e.g. `wendy run --service talker`) must not shrink the cached
// service set, or the monitor stops recognizing the app as a shared-namespace
// group and restarts a wedged secondary against a stale primary PID.
func TestCacheAppServicesLocked_MergeKeepsGroup(t *testing.T) {
	c := &Client{}

	// Initial full-group deploy: two services.
	c.cacheAppServicesLocked("app", map[string]*appconfig.ServiceConfig{
		"talker":   {Context: "./talker"},
		"listener": {Context: "./listener", DependsOn: []string{"talker"}},
	})
	if got := len(c.appServices["app"]); got != 2 {
		t.Fatalf("after full deploy: len = %d, want 2", got)
	}

	// Single-service redeploy of just the primary: must merge, not replace.
	c.cacheAppServicesLocked("app", map[string]*appconfig.ServiceConfig{
		"talker": {Context: "./talker"},
	})
	if got := len(c.appServices["app"]); got != 2 {
		t.Fatalf("after --service talker redeploy: len = %d, want 2 (group must not shrink)", got)
	}
	if _, ok := c.appServices["app"]["listener"]; !ok {
		t.Errorf("secondary 'listener' dropped from cached group after single-service redeploy")
	}
	// The dependsOn graph RestartGroup needs for ordering must be preserved.
	if dep := c.appServices["app"]["listener"].DependsOn; len(dep) != 1 || dep[0] != "talker" {
		t.Errorf("listener.DependsOn = %v, want [talker]", dep)
	}
}
