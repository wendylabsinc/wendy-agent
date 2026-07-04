package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestNotifyCrashFixPrintsAndClears(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{
		PendingFixNotices: []config.FixNotice{{TrackingID: "WDY-ABC123", FixedInRelease: "v1.4.0"}},
	}})
	var buf bytes.Buffer
	cmd := &cobra.Command{Use: "wendy"}
	cmd.SetErr(&buf)
	notifyCrashFix(cmd)
	if !strings.Contains(buf.String(), "WDY-ABC123") || !strings.Contains(buf.String(), "v1.4.0") {
		t.Errorf("notice not printed: %q", buf.String())
	}
	loaded, _ := config.Load()
	if loaded.CrashReport != nil && len(loaded.CrashReport.PendingFixNotices) != 0 {
		t.Errorf("notices not cleared: %+v", loaded.CrashReport.PendingFixNotices)
	}
}
