package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// drive applies a sequence of messages to a StepsModel and returns the result.
func drive(m StepsModel, msgs ...any) StepsModel {
	for _, msg := range msgs {
		mm, _ := m.Update(msg)
		m = mm.(StepsModel)
	}
	return m
}

func TestStepsModel_RunningShowsDetailThenElapsed(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepStartMsg{ID: 0, Label: "Download flashpack"},
		StepDetailMsg{ID: 0, Detail: "1.2/3.0 GiB"},
	)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "Download flashpack") || !strings.Contains(view, "1.2/3.0 GiB") {
		t.Fatalf("running step should show label + detail, got:\n%s", view)
	}

	// Clearing the detail falls back to the elapsed timer, not a stale byte count.
	m = drive(m, StepDetailMsg{ID: 0, Detail: ""})
	if v := ansi.Strip(m.View()); strings.Contains(v, "GiB") {
		t.Fatalf("cleared detail should drop byte count, got:\n%s", v)
	}
}

func TestStepsModel_TerminalStates(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepStartMsg{ID: 0, Label: "Download flashpack"},
		StepDoneMsg{ID: 0, Cached: true},
		StepStartMsg{ID: 1, Label: "Stage 1"},
		StepDoneMsg{ID: 1},
		StepStartMsg{ID: 2, Label: "Stage 2"},
		StepFailMsg{ID: 2},
	)
	view := ansi.Strip(m.View())
	for _, want := range []string{"⚡", "cached", "✓", "Stage 1", "✗", "Stage 2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected %q in view:\n%s", want, view)
		}
	}
}

func TestStepsModel_DoneRendersEmpty(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepStartMsg{ID: 0, Label: "Download flashpack"},
		StepsDoneMsg{Err: nil},
	)
	if v := m.View(); v != "" {
		t.Fatalf("finished model should render empty, got: %q", v)
	}
}

func TestStepsModel_ErrPropagates(t *testing.T) {
	m := drive(NewStepsModel("Flashing"), StepsDoneMsg{Err: ErrCancelled})
	if m.Err() != ErrCancelled {
		t.Fatalf("Err() = %v, want ErrCancelled", m.Err())
	}
}

func TestPadLabel(t *testing.T) {
	if got := padLabel("abc", 6); got != "abc   " {
		t.Errorf("padLabel pad = %q, want %q", got, "abc   ")
	}
	if got := padLabel("abcdef", 6); got != "abcdef" {
		t.Errorf("padLabel exact = %q, want %q", got, "abcdef")
	}
	if got := padLabel("abcdefgh", 6); got != "abcde…" {
		t.Errorf("padLabel truncate = %q, want %q", got, "abcde…")
	}
}

func TestStepsModel_CtrlCWithoutGuardCancelsImmediately(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepStartMsg{ID: 0, Label: "Download flashpack"},
	)
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = mm.(StepsModel)
	if m.Err() == nil || cmd == nil {
		t.Fatal("unguarded ctrl+c should cancel and quit")
	}
}

func TestStepsModel_AbortGuardNeedsSecondCtrlC(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepAbortGuardMsg{Warning: "aborting can leave the Thor unbootable"},
		StepStartMsg{ID: 2, Label: "Stage 2"},
	)

	// First ctrl+c: swallowed, warning shown, no quit.
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = mm.(StepsModel)
	if m.Err() != nil || cmd != nil {
		t.Fatal("first ctrl+c on a guarded step should not cancel")
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "unbootable") {
		t.Fatalf("armed guard should show the warning, got:\n%s", v)
	}

	// Second ctrl+c inside the window: cancels.
	mm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = mm.(StepsModel)
	if m.Err() == nil || cmd == nil {
		t.Fatal("second ctrl+c should cancel")
	}
}

func TestStepsModel_ClearedGuardRestoresInstantCancel(t *testing.T) {
	m := drive(NewStepsModel("Flashing"),
		StepAbortGuardMsg{Warning: "dangerous"},
		StepStartMsg{ID: 2, Label: "Stage 2"},
		StepAbortGuardMsg{}, // next step is safe again
	)
	mm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = mm.(StepsModel)
	if m.Err() == nil || cmd == nil {
		t.Fatal("clearing the guard should restore instant cancel")
	}
}
