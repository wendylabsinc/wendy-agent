package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
)

func agentColumnIndex(t *testing.T, m PickerModel) int {
	t.Helper()
	for i, c := range m.table.Columns() {
		if c.Title == "Agent" {
			return i
		}
	}
	t.Fatalf("no Agent column; columns=%v", m.table.Columns())
	return -1
}

func TestPickerModel_StartsSpinner(t *testing.T) {
	if NewPicker().Init() == nil {
		t.Fatal("Init should start the probe spinner (non-nil command)")
	}
}

func TestPickerModel_PendingRowShowsSpinnerFrame(t *testing.T) {
	m := NewPicker()
	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{{
		Name: "dev", Type: "LAN", Address: "dev.local:50052",
		DedupKey: "dev", Probe: ProbePending,
	}}})
	pm := updated.(PickerModel)

	rows := pm.table.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	if cell := rows[0][agentColumnIndex(t, pm)]; cell == "" {
		t.Fatal("pending row Agent cell should show a spinner frame, got empty")
	}
}

func TestPickerModel_SpinnerTickAdvancesFrame(t *testing.T) {
	m := NewPicker()
	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{{
		Name: "dev", Type: "LAN", Address: "dev.local:50052",
		DedupKey: "dev", Probe: ProbePending,
	}}})
	pm := updated.(PickerModel)
	agentIdx := agentColumnIndex(t, pm)
	first := pm.table.Rows()[0][agentIdx]

	// Drive several ticks; the spinner frame must change at least once.
	changed := false
	for i := 0; i < 12; i++ {
		u, cmd := pm.Update(spinner.TickMsg{})
		pm = u.(PickerModel)
		if cmd == nil {
			t.Fatal("spinner tick should schedule the next tick")
		}
		if pm.table.Rows()[0][agentIdx] != first {
			changed = true
			break
		}
	}
	if !changed {
		t.Fatalf("spinner frame never changed across ticks (stuck on %q)", first)
	}
}

func TestPickerModel_ViewColorizesFailedGlyph(t *testing.T) {
	m := NewPicker()
	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{{
		Name: "dev", Type: "LAN", Address: "dev.local:50052",
		DedupKey: "dev", Probe: ProbeFailed,
	}}})
	pm := updated.(PickerModel)

	view := pm.View()
	if !strings.Contains(view, probeFailedColored) {
		t.Fatalf("View should render the failed glyph in red; view=%q", view)
	}
}
