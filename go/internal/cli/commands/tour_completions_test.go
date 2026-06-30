package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestTourAdvanceOffersCompletionsWhenMissing(t *testing.T) {
	withTourCompletionsInstalled(t, false)
	m := newTourWizardModel()
	// No AI tools detected funnels through advanceToDeviceSetup.
	got, _ := stepTour(t, m, tourAICheckDoneMsg{})
	if got.phase != phaseCompletions {
		t.Fatalf("phase = %v, want phaseCompletions", got.phase)
	}
	if got.menuCursor != 0 {
		t.Errorf("menuCursor = %d, want 0", got.menuCursor)
	}
}

func TestTourAdvanceSkipsCompletionsWhenInstalled(t *testing.T) {
	withTourCompletionsInstalled(t, true)
	m := newTourWizardModel()
	got, cmd := stepTour(t, m, tourAICheckDoneMsg{})
	if got.phase != phaseLoadDevices {
		t.Fatalf("phase = %v, want phaseLoadDevices", got.phase)
	}
	if cmd == nil {
		t.Error("expected loadDevicesCmd")
	}
}

func TestTourCompletionsPhaseYesInstalls(t *testing.T) {
	m := newTourWizardModel()
	m.phase = phaseCompletions
	m.menuCursor = 0
	got, cmd := stepTour(t, m, key("enter"))
	// Stays on the phase until the install subprocess reports back.
	if got.phase != phaseCompletions {
		t.Errorf("phase = %v, want phaseCompletions (awaiting install)", got.phase)
	}
	if cmd == nil {
		t.Error("expected an install command")
	}
}

func TestTourCompletionsPhaseNoSkips(t *testing.T) {
	m := newTourWizardModel()
	m.phase = phaseCompletions
	m.menuCursor = 1
	got, cmd := stepTour(t, m, key("enter"))
	if got.phase != phaseLoadDevices {
		t.Fatalf("phase = %v, want phaseLoadDevices", got.phase)
	}
	if cmd == nil {
		t.Error("expected loadDevicesCmd")
	}
}

func TestTourInstallCompletionsInProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("SHELL", "/bin/bash")
	for _, k := range []string{"ZDOTDIR", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}

	m := newTourWizardModel()
	m.root = NewRootCmd()
	cmd := m.cmdInstallCompletions()
	if cmd == nil {
		t.Fatal("expected an install command")
	}

	msg := cmd()
	done, ok := msg.(tourCompletionInstallDoneMsg)
	if !ok {
		t.Fatalf("msg = %T, want tourCompletionInstallDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("in-process install error: %v", done.err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.CompletionInstalled {
		t.Error("CompletionInstalled = false after in-process install; want true")
	}
}

func TestTourCompletionInstallDoneProceeds(t *testing.T) {
	m := newTourWizardModel()
	m.phase = phaseCompletions
	got, cmd := stepTour(t, m, tourCompletionInstallDoneMsg{})
	if got.phase != phaseLoadDevices {
		t.Fatalf("phase = %v, want phaseLoadDevices", got.phase)
	}
	if cmd == nil {
		t.Error("expected loadDevicesCmd")
	}
	// Even on install error, the tour proceeds (completions are optional).
	got, _ = stepTour(t, m, tourCompletionInstallDoneMsg{err: errTest})
	if got.phase != phaseLoadDevices {
		t.Errorf("phase = %v, want phaseLoadDevices on install error", got.phase)
	}
}
