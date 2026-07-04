package tui

import (
	"strings"
	"testing"
)

func progressAfterUpdate(t *testing.T, msg ProgressUpdateMsg) ProgressModel {
	t.Helper()
	m, _ := NewProgress("Writing...").Update(msg)
	return m.(ProgressModel)
}

func TestProgressViewByteInfo(t *testing.T) {
	t.Run("renders written and total when both known", func(t *testing.T) {
		m := progressAfterUpdate(t, ProgressUpdateMsg{
			Percent: 0.25,
			Written: 50 * 1024 * 1024,
			Total:   200 * 1024 * 1024,
		})
		if view := m.View(); !strings.Contains(view, "(50.0 MiB / 200.0 MiB)") {
			t.Errorf("View() = %q; want it to contain %q", view, "(50.0 MiB / 200.0 MiB)")
		}
	})

	t.Run("renders written alone when total is unknown", func(t *testing.T) {
		m := progressAfterUpdate(t, ProgressUpdateMsg{
			Percent: 0.25,
			Written: 50 * 1024 * 1024,
		})
		view := m.View()
		if !strings.Contains(view, "(50.0 MiB)") {
			t.Errorf("View() = %q; want it to contain %q", view, "(50.0 MiB)")
		}
		// A total renders as "(written / total)"; match the separator
		// specifically so an unrelated "/" in a rotating hint (e.g.
		// "Claude/Codex") doesn't trip this check.
		if strings.Contains(view, " / ") {
			t.Errorf("View() = %q; must not render a total when none is known", view)
		}
	})

	t.Run("done with unknown total keeps written-only counter", func(t *testing.T) {
		m := progressAfterUpdate(t, ProgressUpdateMsg{
			Percent: 1.0,
			Written: 50 * 1024 * 1024,
		})
		next, _ := m.Update(ProgressDoneMsg{})
		if view := next.View(); !strings.Contains(view, "(50.0 MiB)") {
			t.Errorf("View() after done = %q; want it to contain %q", view, "(50.0 MiB)")
		}
	})
}
