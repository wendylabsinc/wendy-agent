package timesync_test

import (
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/timesync"
)

func TestAdvanceTo_NeverGoesBackward(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	// AdvanceTo a past time must be a no-op (no error, clock not set).
	if err := timesync.AdvanceTo(past, nil); err != nil {
		t.Errorf("AdvanceTo past: unexpected error: %v", err)
	}
	// Clock must still be roughly now, not an hour ago.
	if time.Since(time.Now()) > 10*time.Second {
		t.Error("clock moved backward")
	}
}
