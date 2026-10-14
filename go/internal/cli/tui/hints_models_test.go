package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

const hintMarker = "💡"

func TestSpinnerShowsHintWhileRunning(t *testing.T) {
	m := NewSpinner("Building...")
	if !strings.Contains(m.View(), hintMarker) {
		t.Fatal("expected running spinner View to include a hint")
	}
}

func TestSpinnerHidesHintWhenDone(t *testing.T) {
	m := NewSpinner("Building...")
	updated, _ := m.Update(SpinnerDoneMsg{})
	if strings.Contains(updated.View(), hintMarker) {
		t.Fatal("expected completed spinner View to omit the hint")
	}
}

func TestProgressShowsHintWhileRunning(t *testing.T) {
	m := NewProgress("Downloading...")
	if !strings.Contains(m.View(), hintMarker) {
		t.Fatal("expected running progress View to include a hint")
	}
}

func TestProgressHidesHintWhenDone(t *testing.T) {
	m := NewProgress("Downloading...")
	updated, _ := m.Update(ProgressDoneMsg{})
	if strings.Contains(updated.View(), hintMarker) {
		t.Fatal("expected completed progress View to omit the hint")
	}
}

func TestMultiSpinnerShowsHintWhileRunning(t *testing.T) {
	m := NewMultiSpinner("Building 2 services...", []string{"api", "web"})
	if !strings.Contains(m.View(), hintMarker) {
		t.Fatal("expected running multispinner View to include a hint")
	}
}

func TestMultiSpinnerHidesHintWhenDone(t *testing.T) {
	m := NewMultiSpinner("Building 2 services...", []string{"api", "web"})
	updated, _ := m.Update(MultiSpinnerAllDoneMsg{})
	if strings.Contains(updated.View(), hintMarker) {
		t.Fatal("expected completed multispinner View to omit the hint")
	}
}

func TestHintTickRotatesAndReschedules(t *testing.T) {
	m := NewSpinner("Building...")
	updated, cmd := m.Update(hintTickMsg{})
	// A non-nil command is returned to reschedule the next rotation tick.
	// (We don't invoke it: tea.Tick blocks for the full interval.)
	if cmd == nil {
		t.Fatal("expected hintTickMsg handling to reschedule another tick")
	}
	if !strings.Contains(updated.View(), hintMarker) {
		t.Fatal("expected spinner to still show a hint after a tick")
	}
}

func TestSuppressedHintsKeepProgressAcrossModels(t *testing.T) {
	t.Setenv("WENDY_NO_PROGRESS_HINTS", "1")
	for _, tc := range []struct {
		name   string
		model  tea.Model
		update tea.Msg
		want   string
	}{
		{"spinner", NewSpinner("Starting simulator"), SpinnerUpdateMsg{Label: "Waiting for simulator"}, "Waiting for simulator"},
		{"progress", NewProgress("Downloading image"), ProgressUpdateMsg{Percent: .5, Detail: "Receiving image data"}, "Receiving image data"},
		{"multispinner", NewMultiSpinner("Building runtime", []string{"robot"}), MultiSpinnerStartMsg{Name: "robot"}, "building..."},
		{"build steps", NewBuildStepsModel("Building runtime"), BuildStepMsg{ID: "1", Display: "Downloading dependencies", Status: BuildStepRunning}, "Downloading dependencies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, _ := tc.model.Update(tc.update)
			model, tick := model.Update(hintTickMsg{})
			if tick != nil {
				t.Fatal("suppressed hints scheduled a rotation tick")
			}
			view := model.View()
			if strings.Contains(view, hintMarker) || strings.Contains(view, "Tip:") || !strings.Contains(view, tc.want) {
				t.Fatalf("view should preserve progress %q without a hint: %q", tc.want, view)
			}
		})
	}
}

func TestSuppressedHintsKeepErrors(t *testing.T) {
	t.Setenv("WENDY_NO_PROGRESS_HINTS", "1")
	want := errors.New("download failed")
	for _, tc := range []struct {
		model tea.Model
		done  tea.Msg
	}{
		{NewSpinner("Starting simulator"), SpinnerDoneMsg{Err: want}},
		{NewProgress("Downloading image"), ProgressDoneMsg{Err: want}},
	} {
		model, _ := tc.model.Update(tc.done)
		if view := model.View(); !strings.Contains(view, want.Error()) {
			t.Fatalf("suppressed hints hid error: %q", view)
		}
	}
}
