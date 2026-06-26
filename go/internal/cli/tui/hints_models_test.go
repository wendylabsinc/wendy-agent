package tui

import (
	"strings"
	"testing"
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
