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
