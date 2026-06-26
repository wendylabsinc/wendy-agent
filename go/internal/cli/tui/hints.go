package tui

import (
	"math/rand"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// hintInterval is how often a running progress indicator rotates to a new hint.
const hintInterval = 7 * time.Second

// ProgressHints are short, educational "did you know" tips surfaced while a
// progress indicator is running. They give users a heads-up on what the Wendy
// CLI can do during otherwise-idle wait time. Edit this list freely.
var ProgressHints = []string{
	"Tip: Stream live app output with 'wendy device logs'",
	"Tip: 'wendy run' rebuilds and redeploys automatically as you edit files",
	"Tip: Inspect a device's hardware and capabilities with 'wendy device info'",
	"Tip: Update the on-device agent with 'wendy device update'",
	"Tip: Discover devices on your network with 'wendy device discover'",
	"Tip: List and manage running apps with 'wendy device apps list'",
	"Tip: Scaffold a new project in seconds with 'wendy init'",
	"Tip: Grant hardware access (GPU, camera, GPIO) via entitlements in wendy.json",
	"Tip: Validate your wendy.json against the schema with 'wendy json'",
	"Tip: View metrics, traces, and logs with the 'wendy telemetry' commands",
	"Tip: Reach a device behind NAT with 'wendy cloud run'",
	"Tip: Let Claude/Codex drive your devices - set up the MCP server with 'wendy mcp setup'",
	"Tip: Let Claude/Codex help you debug - set up the MCP server with 'wendy mcp setup'",
}

// hintTickMsg is emitted on each hint rotation tick.
type hintTickMsg struct{}

var hintStyle = lipgloss.NewStyle().Foreground(ColorDim)

// hintRotator holds the currently displayed hint and rotates through a list.
type hintRotator struct {
	hints   []string
	current string
}

// newHintRotator builds a rotator over ProgressHints with a random first hint.
func newHintRotator() hintRotator {
	r := hintRotator{hints: ProgressHints}
	if len(r.hints) > 0 {
		r.current = r.hints[rand.Intn(len(r.hints))]
	}
	return r
}

// tick returns a command that fires a hintTickMsg after hintInterval.
func (r hintRotator) tick() tea.Cmd {
	return tea.Tick(hintInterval, func(time.Time) tea.Msg {
		return hintTickMsg{}
	})
}

// next advances to a different random hint. It is a no-op when there are fewer
// than two hints to choose from.
func (r *hintRotator) next() {
	if len(r.hints) < 2 {
		return
	}
	for {
		candidate := r.hints[rand.Intn(len(r.hints))]
		if candidate != r.current {
			r.current = candidate
			return
		}
	}
}

// view renders the current hint as a dimmed line, or "" when there is no hint.
func (r hintRotator) view() string {
	if r.current == "" {
		return ""
	}
	return hintStyle.Render("💡 " + r.current)
}
