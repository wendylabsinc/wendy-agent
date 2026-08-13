package tui

import (
	"strings"
	"testing"
	"time"
)

func TestMultiSpinnerDoneRowShowsCacheCounts(t *testing.T) {
	m := NewMultiSpinner("Building 1 service(s)...", []string{"api"})
	next, _ := m.Update(MultiSpinnerStartMsg{Name: "api"})
	m = next.(MultiSpinnerModel)
	next, _ = m.Update(MultiSpinnerDoneMsg{Name: "api", Dur: 21300 * time.Millisecond, Cached: 4, Rebuilt: 2})
	m = next.(MultiSpinnerModel)
	v := m.View()
	if !strings.Contains(v, "4 cached") || !strings.Contains(v, "2 rebuilt") {
		t.Fatalf("done row missing cache counts:\n%s", v)
	}
}

func TestMultiSpinnerLongServiceNamesStayOnOneLine(t *testing.T) {
	names := []string{"health-high-low", "transcription", "website-frontend"}
	m := NewMultiSpinner("Building 3 service(s)...", names)

	v := m.View()
	lines := strings.Split(strings.TrimSuffix(v, "\n"), "\n")
	// The title, one line per service, and one hint. A fixed-width lipgloss
	// style used to wrap every name longer than 12 cells into an extra line.
	if got, want := len(lines), len(names)+2; got != want {
		t.Fatalf("view has %d lines, want %d:\n%s", got, want, v)
	}
	for _, name := range names {
		if got := strings.Count(v, name); got != 1 {
			t.Errorf("service name %q appears %d times in view:\n%s", name, got, v)
		}
	}
}
