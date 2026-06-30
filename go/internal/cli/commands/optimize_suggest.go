package commands

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/optimize"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// slowIncrementalBuildThreshold is the wall-clock above which an incremental
// build (one that reused cached layers) is slow enough to warrant surfacing an
// optimize scan.
const slowIncrementalBuildThreshold = 50 * time.Second

// maybeSuggestOptimizeAfterBuild runs a static optimize scan and, if it finds
// fixable issues, offers to apply them — but only after a slow incremental
// build, and only on an interactive terminal. The fixes take effect on the
// NEXT build; the current run continues unchanged. In CI / non-interactive
// shells this is a no-op.
func maybeSuggestOptimizeAfterBuild(tally tui.BuildTally, elapsed time.Duration) {
	if !isInteractiveTerminal() {
		return // CI / non-interactive: skip entirely.
	}
	// Only a slow build that actually reused cached layers (i.e. incremental).
	if tally.Cached == 0 || elapsed < slowIncrementalBuildThreshold {
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	cfg, _ := loadOptConfig(cwd)
	targets, err := optimize.DiscoverTargets(cwd, cfg, "arm64")
	if err != nil || len(targets) == 0 {
		return
	}
	findings := optimize.Analyze(targets, optimize.DefaultAnalyzers())
	if len(findings) == 0 {
		return
	}
	rep := optimize.BuildReport(targets, findings)
	_, _, _, fixable := rep.Counts()

	// Surfacing the scan counts as the day's optimize nudge for this project.
	recordOptimizeTipShown(cwd)

	cliNotice("\nThis incremental build took %s. A quick scan found %d build-config issue(s):",
		elapsed.Round(time.Second), len(findings))
	fmt.Fprint(os.Stderr, optimize.RenderHuman(rep))

	if fixable == 0 {
		return
	}

	confirmed, err := tui.ConfirmDefaultYes(
		fmt.Sprintf("Apply %d safe fix(es) now? (takes effect on your next build)", fixable),
		tea.WithOutput(os.Stderr),
	)
	if err != nil || !confirmed {
		return
	}

	applied, err := optimize.ApplyFixes(findings)
	if err != nil {
		cliNotice("Could not apply fixes: %v", err)
		return
	}
	n := 0
	for _, a := range applied {
		if a.Applied {
			n++
		}
	}
	cliSuccess("Applied %d fix(es) — they take effect on your next build.", n)
}

// maybeShowOptimizeTip surfaces a one-line, throttled tip pointing users at
// `wendy project optimize` after a successful build/run. Once per day per
// project, interactive only.
func maybeShowOptimizeTip(cmd *cobra.Command) {
	switch cmd.Name() {
	case "run", "build":
	default:
		return
	}
	if jsonOutput || !isInteractiveTerminal() {
		return
	}
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	if optimizeTipShownToday(cwd) {
		return
	}
	cliNotice("Tip: run `wendy project optimize` to check your Dockerfile and dependencies for build-speed and runtime wins.")
	recordOptimizeTipShown(cwd)
}

func optimizeTipToday() string {
	return time.Now().Format("2006-01-02")
}

func optimizeTipShownToday(projectKey string) bool {
	cfg, err := config.Load()
	if err != nil || cfg.OptimizeTipShownAt == nil {
		return false
	}
	return cfg.OptimizeTipShownAt[projectKey] == optimizeTipToday()
}

func recordOptimizeTipShown(projectKey string) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	if cfg.OptimizeTipShownAt == nil {
		cfg.OptimizeTipShownAt = map[string]string{}
	}
	cfg.OptimizeTipShownAt[projectKey] = optimizeTipToday()
	_ = config.Save(cfg)
}
