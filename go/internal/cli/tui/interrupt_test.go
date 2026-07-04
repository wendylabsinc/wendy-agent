package tui

import (
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const hintRune = "💡"

// TestInterruptFilterConvertsInterruptToCtrlC verifies the filter rewrites an
// InterruptMsg into a Ctrl+C key press (which every progress model already
// handles as a graceful quit) and passes other messages through untouched.
func TestInterruptFilterConvertsInterruptToCtrlC(t *testing.T) {
	got := InterruptFilter(nil, tea.InterruptMsg{})
	key, ok := got.(tea.KeyMsg)
	if !ok || key.Type != tea.KeyCtrlC {
		t.Fatalf("expected InterruptMsg to become Ctrl+C KeyMsg, got %#v", got)
	}

	passthrough := SpinnerUpdateMsg{Label: "x"}
	if got := InterruptFilter(nil, passthrough); got != tea.Msg(passthrough) {
		t.Fatalf("expected non-interrupt message to pass through unchanged, got %#v", got)
	}
}

// quits reports whether a command (possibly a tea.Batch) resolves to tea.Quit.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		return slices.ContainsFunc(msg, quits)
	}
	return false
}

// TestInterruptClearsHintForEveryProgressModel is the regression test for the
// lingering-hint bug. On interrupt, NewProgressProgram routes the InterruptMsg
// through the model's Ctrl+C handling. This asserts the mechanism the fix relies
// on for each of the four hint-bearing models: feeding the filtered interrupt
// message makes the model (a) quit and (b) render a final View that no longer
// contains the rotating hint — so Bubble Tea's graceful path draws a cleared
// frame instead of leaving the hint on screen.
func TestInterruptClearsHintForEveryProgressModel(t *testing.T) {
	cases := []struct {
		name  string
		model tea.Model
	}{
		{"spinner", NewSpinner("Connecting...")},
		{"progress", NewProgress("Downloading...")},
		{"multispinner", NewMultiSpinner("Building...", []string{"api", "web"})},
		{"buildsteps", NewBuildStepsModel("Building image...")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Sanity: while running, the model shows a hint (otherwise the test
			// would pass vacuously).
			if !strings.Contains(tc.model.View(), hintRune) {
				t.Fatalf("%s: expected a hint while running", tc.name)
			}

			filtered := InterruptFilter(tc.model, tea.InterruptMsg{})
			updated, cmd := tc.model.Update(filtered)

			if !quits(cmd) {
				t.Fatalf("%s: interrupt should make the model quit gracefully", tc.name)
			}
			if strings.Contains(updated.View(), hintRune) {
				t.Fatalf("%s: hint must be gone from the final View after interrupt", tc.name)
			}
		})
	}
}
