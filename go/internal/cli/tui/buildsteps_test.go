package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func contains(s, sub string) bool { return strings.Contains(s, sub) }

type tErr string

func (e tErr) Error() string    { return string(e) }
func errForTest(s string) error { return tErr(s) }
func keyMsg(s string) tea.KeyMsg {
	// minimal: only ctrl+c is asserted; map it explicitly.
	if s == "ctrl+c" {
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func applyBuild(m BuildStepsModel, msgs ...interface{}) BuildStepsModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(BuildStepsModel)
	}
	return m
}

func TestBuildStepsModelTracksTally(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	m = applyBuild(m,
		BuildStepMsg{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM", Status: BuildStepRunning},
		BuildStepMsg{ID: 6, Kind: BuildVertexStep, Display: "[1/6] FROM", Status: BuildStepCached},
		BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN", Status: BuildStepRunning},
		BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN", Status: BuildStepDone, Dur: time.Second},
	)
	if got := m.Tally(); got.Cached != 1 || got.Rebuilt != 1 {
		t.Fatalf("tally = %+v, want {1 1}", got)
	}
}

func TestBuildStepsModelViewShowsActiveStep(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	m = applyBuild(m, BuildStepMsg{ID: 9, Kind: BuildVertexStep, Display: "[4/6] RUN pip install", Status: BuildStepRunning})
	if v := m.View(); !contains(v, "[4/6] RUN pip install") {
		t.Fatalf("view missing active step:\n%s", v)
	}
}

func TestBuildStepsModelAllDoneQuitsAndKeepsErr(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	wantErr := errForTest("boom")
	next, cmd := m.Update(BuildAllDoneMsg{Err: wantErr})
	m = next.(BuildStepsModel)
	if cmd == nil {
		t.Fatal("expected a quit command")
	}
	if m.Err() != wantErr {
		t.Fatalf("Err() = %v, want %v", m.Err(), wantErr)
	}
}

func TestBuildStepsModelCtrlCCancels(t *testing.T) {
	m := NewBuildStepsModel("Building image...")
	next, _ := m.Update(keyMsg("ctrl+c"))
	m = next.(BuildStepsModel)
	if m.Err() != ErrCancelled {
		t.Fatalf("Err() = %v, want ErrCancelled", m.Err())
	}
}
