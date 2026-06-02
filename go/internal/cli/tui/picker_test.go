package tui

import (
	"strings"
	"testing"

	bubbleTable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestPickerModel_SelectsFromTable(t *testing.T) {
	m := NewPickerWithTitle("Select a WiFi network")

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Type: "82%", Value: "alpha"},
		{Name: "beta", Type: "65%", Value: "beta"},
	}})
	pm := updated.(PickerModel)

	view := pm.View()
	for _, want := range []string{"Select a WiFi network", "Name", "Type", "alpha", "82%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected picker view to contain %q, got %q", want, view)
		}
	}

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyDown})
	pm = updated.(PickerModel)

	if pm.table.Cursor() != 1 {
		t.Fatalf("expected cursor on second row, got %d", pm.table.Cursor())
	}

	updated, cmd := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	pm = updated.(PickerModel)

	if cmd == nil {
		t.Fatal("expected enter to return quit command")
	}
	if pm.Selected() == nil {
		t.Fatal("expected selected item after enter")
	}
	if got := pm.Selected().Value.(string); got != "beta" {
		t.Fatalf("selected value = %q, want %q", got, "beta")
	}
}

func TestPickerModel_DedupesItems(t *testing.T) {
	m := NewPicker()

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "wendy-alpha", Type: "LAN", Address: "192.168.1.10", Value: "a"},
	}})
	pm := updated.(PickerModel)

	updated, _ = pm.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "wendy-alpha", Type: "LAN", Address: "192.168.1.10", Value: "b"},
	}})
	pm = updated.(PickerModel)

	if got := len(pm.items); got != 1 {
		t.Fatalf("expected 1 deduped item, got %d", got)
	}
}

func TestPickerModel_ShowsDescriptionColumnWhenPresent(t *testing.T) {
	m := NewPickerWithTitle("Select a target")

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "WendyOS", Description: "Full Linux-based edge device", Value: "wendyos"},
	}})
	pm := updated.(PickerModel)

	view := pm.View()
	for _, want := range []string{"Description", "Full Linux-based edge device"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected picker view to contain %q, got %q", want, view)
		}
	}
}

func TestPickerModel_CustomColumns(t *testing.T) {
	m := NewPickerWithTitleAndColumns("Select a model", []PickerColumn{
		{
			Title:    "model",
			MinWidth: 16,
			Required: true,
			Value: func(item PickerItem) string {
				return item.Name
			},
		},
		{
			Title: "size",
			Value: func(item PickerItem) string {
				return item.Size
			},
		},
		{
			Title: "parameters",
			Value: func(item PickerItem) string {
				return item.Parameters
			},
		},
		{
			Title: "comments",
			Value: func(item PickerItem) string {
				return item.Comments
			},
		},
	})

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{
			Name:       "gemma4:e2b",
			Size:       "edge",
			Parameters: "E2B",
			Comments:   "default for small devices",
			Value:      "gemma4:e2b",
		},
	}})
	pm := updated.(PickerModel)

	view := pm.View()
	for _, want := range []string{"model", "size", "parameters", "comments", "gemma4:e2b", "default for small devices"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected picker view to contain %q, got %q", want, view)
		}
	}
}

func TestPickerModel_CustomColumnsNilFallsBackToDefaultPicker(t *testing.T) {
	m := NewPickerWithTitleAndColumns("Select", nil)

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Description: "only show populated optional columns", Value: "alpha"},
	}})
	pm := updated.(PickerModel)

	if hasColumn(pm.table.Columns(), "Type") {
		t.Fatal("nil custom columns should not force the fixed default column layout")
	}
	if !hasColumn(pm.table.Columns(), "Description") {
		t.Fatal("nil custom columns should preserve the default picker dynamic column behavior")
	}
}

func TestPickerTableData_KeepsFullColumnContentForScrolling(t *testing.T) {
	items := []PickerItem{{
		Name:        "wendyos-sunny-daisy",
		Type:        "USB, LAN",
		Address:     "wendyos-sunny-daisy.local:50052",
		Description: "Full Linux-based edge device",
		Value:       "sunny",
	}}

	cols, rows := PickerTableData(items, "", false)

	for _, want := range []string{"Name", "Type", "Address", "Description"} {
		if !hasColumn(cols, want) {
			t.Fatalf("expected %s column to remain scrollable", want)
		}
	}
	if width := columnWidth(cols, "Address"); width < len("wendyos-sunny-daisy.local:50052") {
		t.Fatalf("address column width = %d, want enough for full hostname", width)
	}
	if len(rows) != 1 || len(rows[0]) != len(cols) {
		t.Fatalf("row/column mismatch: rows=%v cols=%v", rows, cols)
	}
}

func TestPickerTableData_KeepsDefaultMarkerInNarrowWidth(t *testing.T) {
	items := []PickerItem{{
		Name:     "alpha",
		Type:     "LAN",
		DedupKey: "alpha",
		Value:    "alpha",
	}}

	cols, rows := PickerTableData(items, "alpha", true)

	if len(cols) < 2 || cols[0].Title != "" || cols[1].Title != "Name" {
		t.Fatalf("columns = %v, want marker and Name columns", cols)
	}
	if len(rows) != 1 || len(rows[0]) < 1 || rows[0][0] != "★" {
		t.Fatalf("default marker row = %v, want leading star", rows)
	}
}

func TestPickerDeviceTableData_UsesStableDeviceSchema(t *testing.T) {
	cols, rows := PickerDeviceTableData([]PickerItem{{
		Name: "alpha",
	}}, "", false)

	for _, want := range []string{"Name", "Type", "Address", "wendy-agent version", "WendyOS Version", "Description"} {
		if !hasColumn(cols, want) {
			t.Fatalf("expected stable device column %q, got %v", want, cols)
		}
	}
	if len(rows) != 1 || len(rows[0]) != len(cols) {
		t.Fatalf("row/column mismatch: rows=%v cols=%v", rows, cols)
	}
}

func TestNewPicker_UsesStableDeviceSchemaBeforeMetadataArrives(t *testing.T) {
	m := NewPicker()

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{{Name: "alpha", Value: "alpha"}}})
	pm := updated.(PickerModel)

	for _, want := range []string{"Name", "Type", "Address", "wendy-agent version", "WendyOS Version", "Description"} {
		if !hasColumn(pm.table.Columns(), want) {
			t.Fatalf("expected stable device column %q, got %v", want, pm.table.Columns())
		}
	}
}

func TestPickerDeviceTableData_ShowsAgentAndWendyOSVersions(t *testing.T) {
	_, rows := PickerDeviceTableData([]PickerItem{{
		Name:         "alpha",
		Type:         "LAN",
		Address:      "alpha.local:50052",
		AgentVersion: "2026.05.30-161141",
		OSVersion:    "WendyOS-0.10.4",
	}}, "", false)

	if len(rows) != 1 || len(rows[0]) < 5 {
		t.Fatalf("rows = %v, want version cells", rows)
	}
	if rows[0][3] != "2026.05.30-161141" {
		t.Fatalf("agent version cell = %q", rows[0][3])
	}
	if rows[0][4] != "WendyOS-0.10.4" {
		t.Fatalf("WendyOS version cell = %q", rows[0][4])
	}
}

func TestPickerModel_WindowWidthControlsColumns(t *testing.T) {
	m := NewPickerWithTitle("Select a device")

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 20})
	pm := updated.(PickerModel)
	updated, _ = pm.Update(PickerAddMsg{Items: []PickerItem{{
		Name:        "wendyos-sunny-daisy",
		Type:        "USB, LAN",
		Address:     "wendyos-sunny-daisy.local:50052",
		Description: "Full Linux-based edge device",
		Value:       "sunny",
	}}})
	pm = updated.(PickerModel)

	for _, want := range []string{"Name", "Type", "Address", "Description"} {
		if !hasColumn(pm.table.Columns(), want) {
			t.Fatalf("expected narrow picker to keep %s column for scrolling", want)
		}
	}
	for _, line := range strings.Split(pm.tableView(), "\n") {
		if got := ansi.StringWidth(line); got > 24 {
			t.Fatalf("cropped line width = %d, want <= 24: %q", got, line)
		}
	}
	if table := pm.table.FullView(); !strings.Contains(table, "wendyos-sunny-daisy.local:50052") {
		t.Fatalf("underlying table should preserve full address without ellipsis, got %q", table)
	}

	updated, _ = pm.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	pm = updated.(PickerModel)

	if !hasColumn(pm.table.Columns(), "Address") {
		t.Fatal("expected wide picker to show Address column")
	}
	if !hasColumn(pm.table.Columns(), "Description") {
		t.Fatal("expected wide picker to show Description column")
	}
}

func TestPickerModel_LeftRightScrollsWithoutBreakingVerticalNavigation(t *testing.T) {
	m := NewPickerWithTitle("Select a device")

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 20})
	pm := updated.(PickerModel)
	updated, _ = pm.Update(PickerAddMsg{Items: []PickerItem{
		{
			Name:        "wendyos-alpha",
			Type:        "USB, LAN",
			Address:     "wendyos-alpha.local:50052",
			Description: "Full Linux-based edge device",
			Value:       "alpha",
		},
		{
			Name:        "wendyos-beta",
			Type:        "LAN",
			Address:     "wendyos-beta.local:50052",
			Description: "Another device",
			Value:       "beta",
		},
	}})
	pm = updated.(PickerModel)

	if !pm.canScrollTable() {
		t.Fatalf("expected table width %d to exceed viewport %d", PickerTableWidth(pm.table.Columns()), pm.tableViewportWidth())
	}
	before := pm.tableView()

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRight})
	pm = updated.(PickerModel)

	if pm.table.ScrollOffset() == 0 {
		t.Fatal("expected right arrow to advance horizontal offset")
	}
	if after := pm.tableView(); after == before {
		t.Fatal("expected scrolled table view to change")
	}

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyDown})
	pm = updated.(PickerModel)

	if pm.table.Cursor() != 1 {
		t.Fatalf("expected down arrow to move cursor, got %d", pm.table.Cursor())
	}
	if pm.table.ScrollOffset() == 0 {
		t.Fatal("expected vertical navigation to preserve horizontal offset")
	}

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyLeft})
	pm = updated.(PickerModel)

	if pm.table.ScrollOffset() != 0 {
		t.Fatalf("expected left arrow to return to zero offset, got %d", pm.table.ScrollOffset())
	}
}

func TestPickerModel_ShowsUSBInTypeColumn(t *testing.T) {
	m := NewPickerWithTitle("Select a device")

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "Sunny Daisy", Type: "LAN", USB: "WendyOS Device sunny-daisy (en40)", Address: "wendyos-sunny-daisy.local:50052", Value: "sunny"},
	}})
	pm := updated.(PickerModel)

	view := pm.View()
	if !strings.Contains(view, "USB, LAN") {
		t.Fatalf("expected picker view to contain %q, got %q", "USB, LAN", view)
	}
}

func TestPickerModel_ShowsSelectedHintAtBottom(t *testing.T) {
	dockerHint := "Hint: Use Docker Desktop for local container or Compose runs when you do not need WendyOS hardware."
	localHint := "Hint: Use Local Machine for native Swift, Go, or Python apps that should run directly on this computer."
	m := NewPicker()

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "Docker Desktop", Type: "Docker Desktop", Hint: dockerHint, Value: "docker"},
		{Name: "Local Machine", Type: "This Device", Hint: localHint, Value: "local"},
	}})
	pm := updated.(PickerModel)

	plain := ansi.Strip(pm.View())
	if got := lastNonEmptyLine(plain); got != dockerHint {
		t.Fatalf("footer hint = %q, want %q", got, dockerHint)
	}
	if strings.Contains(plain, localHint) {
		t.Fatalf("unexpected non-selected hint %q in view %q", localHint, plain)
	}

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyDown})
	pm = updated.(PickerModel)

	plain = ansi.Strip(pm.View())
	if got := lastNonEmptyLine(plain); got != localHint {
		t.Fatalf("footer hint after moving cursor = %q, want %q", got, localHint)
	}
	if strings.Contains(plain, dockerHint) {
		t.Fatalf("unexpected previous hint %q in view %q", dockerHint, plain)
	}
}

func TestPickerModel_DefaultKeyShowsStar(t *testing.T) {
	m := NewPickerWithTitle("Select a device")
	m.DefaultKey = "alpha"
	m.OnSetDefault = func(item PickerItem) {}
	m.OnUnsetDefault = func() {}

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Type: "LAN", Value: "alpha"},
		{Name: "beta", Type: "LAN", Value: "beta"},
	}})
	pm := updated.(PickerModel)

	view := pm.View()
	if !strings.Contains(view, "★") {
		t.Error("expected ★ indicator for default item")
	}
	if !strings.Contains(view, "d set default") {
		t.Error("expected hint text to contain 'd set default'")
	}
	if !strings.Contains(view, "x unset default") {
		t.Error("expected hint text to contain 'x unset default'")
	}
}

func TestPickerTableData_DefaultKeysShowStar(t *testing.T) {
	_, rows := PickerTableData([]PickerItem{{
		Name:        "ubuntu",
		Type:        "LAN",
		DedupKey:    "ubuntu",
		DefaultKeys: []string{"ubuntu.local"},
	}}, "ubuntu.local", true)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0][0] != "★" {
		t.Fatalf("default marker = %q, want ★", rows[0][0])
	}
}

func TestPickerModel_DKeySetsDefault(t *testing.T) {
	m := NewPickerWithTitle("Select a device")
	var setItem PickerItem
	m.OnSetDefault = func(item PickerItem) { setItem = item }
	m.OnUnsetDefault = func() {}

	// Add items.
	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Type: "LAN", Value: "alpha"},
		{Name: "beta", Type: "LAN", Value: "beta"},
	}})
	pm := updated.(PickerModel)

	// Press 'd' on the first item (cursor starts at 0).
	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	pm = updated.(PickerModel)

	if setItem.Name != "alpha" {
		t.Errorf("OnSetDefault called with %q, want alpha", setItem.Name)
	}
	if pm.DefaultKey != "alpha" {
		t.Errorf("DefaultKey = %q, want alpha", pm.DefaultKey)
	}
}

func TestPickerModel_XKeyClearsDefault(t *testing.T) {
	m := NewPickerWithTitle("Select a device")
	m.DefaultKey = "alpha"
	var unsetCalled bool
	m.OnSetDefault = func(item PickerItem) {}
	m.OnUnsetDefault = func() { unsetCalled = true }

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Type: "LAN", Value: "alpha"},
	}})
	pm := updated.(PickerModel)

	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	pm = updated.(PickerModel)

	if !unsetCalled {
		t.Error("OnUnsetDefault was not called")
	}
	if pm.DefaultKey != "" {
		t.Errorf("DefaultKey = %q, want empty", pm.DefaultKey)
	}
}

func TestPickerModel_SetMsgReplacesItems(t *testing.T) {
	m := NewPicker()

	updated, _ := m.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "alpha", Value: "alpha"},
		{Name: "beta", Value: "beta"},
	}})
	pm := updated.(PickerModel)

	updated, _ = pm.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "gamma", Value: "gamma"},
	}})
	pm = updated.(PickerModel)

	if got := len(pm.items); got != 1 {
		t.Fatalf("expected replacement to leave 1 item, got %d", got)
	}
	if got := pm.items[0].Name; got != "gamma" {
		t.Fatalf("remaining item = %q, want gamma", got)
	}
	if _, ok := pm.seenIdx["alpha"]; ok {
		t.Fatal("stale alpha key remained in seenIdx")
	}
}

func TestPickerModel_SetMsgPreservesCursorByItem(t *testing.T) {
	m := NewPicker()

	updated, _ := m.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "alpha", Value: "alpha"},
		{Name: "beta", Value: "beta"},
	}})
	pm := updated.(PickerModel)
	pm.table.SetCursor(1)

	updated, _ = pm.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "beta", Value: "beta"},
		{Name: "alpha", Value: "alpha"},
		{Name: "gamma", Value: "gamma"},
	}})
	pm = updated.(PickerModel)

	cursor := pm.table.Cursor()
	if cursor < 0 || cursor >= len(pm.items) {
		t.Fatalf("cursor out of range: %d", cursor)
	}
	if got := pm.items[cursor].Name; got != "beta" {
		t.Fatalf("cursor item = %q, want beta", got)
	}
}

func TestPickerModel_SetMsgClampsCursorWhenItemRemoved(t *testing.T) {
	m := NewPicker()

	updated, _ := m.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "alpha", Value: "alpha"},
		{Name: "beta", Value: "beta"},
	}})
	pm := updated.(PickerModel)
	pm.table.SetCursor(1)

	updated, _ = pm.Update(PickerSetMsg{Items: []PickerItem{
		{Name: "alpha", Value: "alpha"},
	}})
	pm = updated.(PickerModel)

	if got := pm.table.Cursor(); got != 0 {
		t.Fatalf("cursor = %d, want 0", got)
	}
}

func TestPickerModel_DXIgnoredWithoutCallbacks(t *testing.T) {
	m := NewPickerWithTitle("Select")
	// No OnSetDefault/OnUnsetDefault set.

	updated, _ := m.Update(PickerAddMsg{Items: []PickerItem{
		{Name: "alpha", Value: "alpha"},
	}})
	pm := updated.(PickerModel)

	// Press 'd' — should not panic or set anything.
	updated, _ = pm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	pm = updated.(PickerModel)
	if pm.DefaultKey != "" {
		t.Error("DefaultKey should remain empty without callback")
	}

	// View should NOT contain d/x hint.
	view := pm.View()
	if strings.Contains(view, "d set default") {
		t.Error("d/x hint should not appear without callbacks")
	}
}

func hasColumn(cols []bubbleTable.Column, title string) bool {
	for _, col := range cols {
		if col.Title == title {
			return true
		}
	}
	return false
}

func columnWidth(cols []bubbleTable.Column, title string) int {
	for _, col := range cols {
		if col.Title == title {
			return col.Width
		}
	}
	return 0
}

func lastNonEmptyLine(view string) string {
	lines := strings.Split(strings.TrimSpace(view), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
