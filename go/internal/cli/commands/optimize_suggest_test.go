package commands

import (
	"testing"
)

func TestOptimizeTipThrottle(t *testing.T) {
	// Isolate the config dir to a temp HOME so we don't touch the real ~/.wendy.
	t.Setenv("HOME", t.TempDir())

	const key = "/some/project"

	if optimizeTipShownToday(key) {
		t.Fatalf("tip should not be marked shown before it is recorded")
	}

	recordOptimizeTipShown(key)

	if !optimizeTipShownToday(key) {
		t.Fatalf("tip should be marked shown for today after recording")
	}

	// A different project must not be throttled by the first one's record.
	if optimizeTipShownToday("/other/project") {
		t.Fatalf("throttle must be per-project, not global")
	}
}
