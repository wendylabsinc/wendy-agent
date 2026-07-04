package commands

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/osnotify"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// notifyCrashFix surfaces any pending "your crash was fixed" notices recorded
// by the background status poll: one stderr line each plus a best-effort OS
// notification, then clears them. Best-effort; never errors.
func notifyCrashFix(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil || cfg.CrashReport == nil || len(cfg.CrashReport.PendingFixNotices) == 0 {
		return
	}
	for _, n := range cfg.CrashReport.PendingFixNotices {
		rel := n.FixedInRelease
		if rel == "" {
			rel = "a recent release"
		}
		cmd.PrintErrf("\n✓ A crash you reported (%s) is fixed in %s. Update the CLI to get the fix.\n", n.TrackingID, rel)
		osnotify.Notify("Wendy: crash fixed", fmt.Sprintf("%s fixed in %s", n.TrackingID, rel))
	}
	cfg.CrashReport.PendingFixNotices = nil
	_ = config.Save(cfg)
}
