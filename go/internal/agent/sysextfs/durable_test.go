package sysextfs

import (
	"path/filepath"
	"testing"
)

// The overlay case needs a real sysext merge and is covered on hardware; what is
// worth pinning here is that the check never blocks a write it cannot judge.
func TestCheckDurable(t *testing.T) {
	if err := CheckDurable(t.TempDir()); err != nil {
		t.Errorf("ordinary directory rejected: %v", err)
	}
	if err := CheckDurable(filepath.Join(t.TempDir(), "absent")); err != nil {
		t.Errorf("unstattable directory must not block a write: %v", err)
	}
}
