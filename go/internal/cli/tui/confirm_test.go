package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func stepConfirm(t *testing.T, m ConfirmModel, key string) ConfirmModel {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	cm, ok := next.(ConfirmModel)
	if !ok {
		t.Fatalf("Update returned %T, want ConfirmModel", next)
	}
	return cm
}

func TestConfirmNoDefault_IgnoresEnter(t *testing.T) {
	m := NewConfirmNoDefault("Install?")

	// Enter must not resolve the prompt when there is no default.
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := next.(ConfirmModel)
	if cm.answered {
		t.Error("Enter resolved a no-default prompt; want it ignored")
	}

	// Arrows/tab must not establish a default either.
	cm = stepConfirm(t, cm, "left")
	if cm.choice {
		t.Error("left arrow set a choice on a no-default prompt")
	}
}

func TestConfirmNoDefault_ResolvesOnKey(t *testing.T) {
	yes := stepConfirm(t, NewConfirmNoDefault("Install?"), "y")
	if !yes.answered || !yes.Confirmed() {
		t.Errorf("after 'y': answered=%v confirmed=%v; want both true", yes.answered, yes.Confirmed())
	}

	no := stepConfirm(t, NewConfirmNoDefault("Install?"), "n")
	if !no.answered || no.Confirmed() {
		t.Errorf("after 'n': answered=%v confirmed=%v; want answered=true confirmed=false", no.answered, no.Confirmed())
	}
	if no.Cancelled() {
		t.Error("'n' should not count as cancelled")
	}
}

func TestConfirmNoDefault_CtrlCCancels(t *testing.T) {
	next, _ := NewConfirmNoDefault("Install?").Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	cm := next.(ConfirmModel)
	if !cm.Cancelled() {
		t.Error("Ctrl+C should cancel the prompt")
	}
	if cm.answered {
		t.Error("Ctrl+C should not mark the prompt answered")
	}
}
