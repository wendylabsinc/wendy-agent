package tui

import (
	"fmt"
	"io"
	"time"
)

// BuildTally counts cached vs rebuilt Dockerfile steps for the summary line.
type BuildTally struct {
	Cached  int
	Rebuilt int
}

// formatDuration returns a human-readable duration string with at least one
// decimal place (e.g., "2.0s", "4.3s").
func formatDuration(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Round(time.Millisecond).Seconds())
}

// NewBuildPlainRenderer returns an emit callback (to pass to NewBuildParser) that
// writes one concise line per completed step to w, plus a tally accessor for the
// final summary. It is the non-interactive (CI / piped) renderer.
func NewBuildPlainRenderer(w io.Writer) (func(BuildStepEvent), func() BuildTally) {
	var t BuildTally
	emit := func(e BuildStepEvent) {
		switch e.Status {
		case BuildStepCached:
			if e.Kind == BuildVertexStep {
				t.Cached++
			}
			fmt.Fprintf(w, "  cached  %s\n", e.Display)
		case BuildStepDone:
			if e.Kind == BuildVertexStep {
				t.Rebuilt++
			}
			durStr := formatDuration(e.Dur)
			fmt.Fprintf(w, "  done    %s  %s\n", e.Display, durStr)
		case BuildStepFailed:
			fmt.Fprintf(w, "  FAILED  %s\n", e.Display)
		}
		// BuildStepRunning is intentionally silent in plain mode.
	}
	tally := func() BuildTally { return t }
	return emit, tally
}
