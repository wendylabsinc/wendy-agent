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

func TestConfirmDefaultYes_EnterConfirms(t *testing.T) {
	// Enter with no explicit key resolves to the default (Yes). This is the
	// semantics commands.confirmFn relies on (empty input counts as yes).
	next, _ := NewConfirmDefaultYes("Update?").Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := next.(ConfirmModel)
	if !cm.answered || !cm.Confirmed() {
		t.Errorf("Enter on default-yes: answered=%v confirmed=%v; want both true", cm.answered, cm.Confirmed())
	}
}

func TestConfirmDefaultNo_EnterDeclines(t *testing.T) {
	// Enter resolves to the default (No). This is the semantics
	// commands.confirmDefaultNoFn relies on (empty input counts as no).
	next, _ := NewConfirm("Continue?").Update(tea.KeyMsg{Type: tea.KeyEnter})
	cm := next.(ConfirmModel)
	if !cm.answered {
		t.Error("Enter on default-no should resolve the prompt")
	}
	if cm.Confirmed() {
		t.Error("Enter on default-no should count as no")
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
