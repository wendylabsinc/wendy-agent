package commands

import (
	"context"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const crashStatusCheckInterval = 6 * time.Hour

func dueCrashStatusCheck(cfg *config.Config) bool {
	if cfg.CrashReport == nil || len(cfg.CrashReport.SubscribedReports) == 0 {
		return false
	}
	last := cfg.CrashReport.LastCrashStatusCheck
	if last == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	now := time.Now().UTC()
	if t.After(now) {
		return true
	}
	return now.Sub(t) >= crashStatusCheckInterval
}

// scheduleCrashStatusCheck launches a background poll for crash-fix status.
// The cfg parameter is only used by the caller's dueCrashStatusCheck gate; the
// goroutine below loads its own fresh config copy (mirroring notifyCrashFix /
// notifyCLIUpdate) rather than closing over and mutating the caller's shared
// *config.Config, since scheduleCLIUpdateCheck's goroutine may be racing to
// save the same pointer concurrently.
func scheduleCrashStatusCheck(cfg *config.Config) {
	go func() {
		cfg, err := config.Load()
		if err != nil || cfg.CrashReport == nil {
			return
		}
		anonID, err := analytics.DistinctID()
		if err != nil {
			return
		}
		fixed, ferr := crashreport.FetchStatus(context.Background(), analytics.TelemetryBaseURL()+"/crashreports/status", anonID)
		cfg.CrashReport.LastCrashStatusCheck = time.Now().UTC().Format(time.RFC3339)
		if ferr == nil {
			applyFixedReports(cfg, fixed)
		}
		_ = config.Save(cfg)
	}()
}

// applyFixedReports moves fixed tracking ids from SubscribedReports into
// PendingFixNotices, de-duplicating against notices already pending.
func applyFixedReports(cfg *config.Config, fixed []crashreport.FixedReport) {
	if len(fixed) == 0 {
		return
	}
	pending := map[string]bool{}
	for _, n := range cfg.CrashReport.PendingFixNotices {
		pending[n.TrackingID] = true
	}
	remaining := cfg.CrashReport.SubscribedReports[:0]
	fixedSet := map[string]string{}
	for _, f := range fixed {
		fixedSet[f.TrackingID] = f.FixedInRelease
	}
	for _, id := range cfg.CrashReport.SubscribedReports {
		if rel, ok := fixedSet[id]; ok {
			if !pending[id] {
				cfg.CrashReport.PendingFixNotices = append(cfg.CrashReport.PendingFixNotices, config.FixNotice{TrackingID: id, FixedInRelease: rel})
			}
			continue
		}
		remaining = append(remaining, id)
	}
	cfg.CrashReport.SubscribedReports = remaining
}
