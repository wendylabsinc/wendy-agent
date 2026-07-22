package tui

import (
	"reflect"
	"strings"
	"testing"

	bubbleTable "github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
)

// Regression test for the "wendy discover eats my terminal" bug: bubbletea
// delivers a WindowSizeMsg at every program start, and BubbleTable.Update used
// to answer it with tea.ClearScreen. That emits CSI 2J on the primary screen,
// destroying the user's visible terminal content in place (erased lines never
// reach scrollback). The renderer already repaints on WindowSizeMsg, so no
// command is needed here at all.
func TestBubbleTableWindowSizeMsgMustNotClearScreen(t *testing.T) {
	cols := []bubbleTable.Column{{Title: "Name", Width: 20}}
	tbl := NewBubbleTable(true, cols)

	_, cmd := tbl.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd == nil {
		return
	}
	if msg := cmd(); reflect.TypeOf(msg) == reflect.TypeOf(tea.ClearScreen()) {
		t.Fatal("BubbleTable.Update answered WindowSizeMsg with tea.ClearScreen; " +
			"this wipes the user's visible terminal content at TUI startup and on every resize")
	}
}

// The underlying bubbles table.SetRows() drives its cursor to -1 whenever it is
// called with an empty slice, and never restores it once rows reappear. A
// negative cursor makes the table render zero rows, so an interactive table
// that receives its data after an initial empty SetRows (e.g. a window-resize
// or empty poll landing before the first payload) looks empty until the user
// presses an arrow key. BubbleTable.SetRows must guard against this.
func TestBubbleTableSetRowsRecoversCursorAfterEmpty(t *testing.T) {
	cols := []bubbleTable.Column{{Title: "Name", Width: 20}}
	tbl := NewBubbleTable(true, cols)
	tbl.SetHeight(5)

	// Empty first (the trigger).
	tbl.SetRows(nil)

	// Then real data arrives.
	tbl.SetRows([]bubbleTable.Row{{"my-app"}})

	if cur := tbl.Cursor(); cur < 0 {
		t.Fatalf("cursor = %d after non-empty SetRows, want >= 0", cur)
	}
	if view := tbl.FullView(); !strings.Contains(view, "my-app") {
		t.Fatalf("expected row visible in view without arrow-key input, got:\n%s", view)
	}
}

// A non-empty SetRows should not disturb an already-valid cursor position.
func TestBubbleTableSetRowsPreservesValidCursor(t *testing.T) {
	cols := []bubbleTable.Column{{Title: "Name", Width: 20}}
	tbl := NewBubbleTable(true, cols)
	tbl.SetHeight(10)
	tbl.SetRows([]bubbleTable.Row{{"a"}, {"b"}, {"c"}})
	tbl.SetCursor(2)

	tbl.SetRows([]bubbleTable.Row{{"a"}, {"b"}, {"c"}, {"d"}})
	if cur := tbl.Cursor(); cur != 2 {
		t.Fatalf("cursor = %d after SetRows, want it preserved at 2", cur)
	}
}
