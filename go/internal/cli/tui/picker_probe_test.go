package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestColorizeProbeGlyphs(t *testing.T) {
	t.Run("no glyph is unchanged", func(t *testing.T) {
		in := "alpha  0.10.4  WendyOS-0.10.4"
		if got := ColorizeProbeGlyphs(in); got != in {
			t.Errorf("got %q; want unchanged %q", got, in)
		}
	})

	t.Run("glyph is colored but width preserved", func(t *testing.T) {
		in := "failed  ▲  ▲"
		got := ColorizeProbeGlyphs(in)
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("expected ANSI color escapes in %q", got)
		}
		// Coloring must not change the visible text/width: stripping ANSI must
		// return exactly the original string.
		if stripped := ansi.Strip(got); stripped != in {
			t.Errorf("stripped = %q; want original %q", stripped, in)
		}
		// Must not emit a full reset (\x1b[0m), which would clear a selected
		// row's background; only the foreground is reset.
		if strings.Contains(got, "\x1b[0m") {
			t.Errorf("colorized glyph should not emit a full reset: %q", got)
		}
	})
}

func TestProbeColumnValue(t *testing.T) {
	const frame = "⠙"
	cases := []struct {
		name    string
		state   ProbeState
		version string
		want    string
	}{
		{"none keeps version", ProbeNone, "0.10.4", "0.10.4"},
		{"none empty stays empty", ProbeNone, "", ""},
		{"pending shows spinner frame", ProbePending, "", frame},
		{"pending ignores stale version", ProbePending, "0.10.4", frame},
		{"ok shows version", ProbeOK, "0.10.4", "0.10.4"},
		{"failed without version shows triangle", ProbeFailed, "", ProbeFailedGlyph},
		{"failed with cached version keeps it", ProbeFailed, "0.10.4", "0.10.4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := probeColumnValue(tc.state, tc.version, frame)
			if got != tc.want {
				t.Errorf("probeColumnValue(%v, %q, %q) = %q; want %q", tc.state, tc.version, frame, got, tc.want)
			}
		})
	}
}

func TestPickerDeviceTableData_ProbeStates(t *testing.T) {
	const frame = "⠹"
	items := []PickerItem{
		{Name: "pending", Type: "LAN", Address: "p.local:50052", Probe: ProbePending, ProbeFrame: frame},
		{Name: "failed", Type: "LAN", Address: "f.local:50052", Probe: ProbeFailed},
		{Name: "ok", Type: "LAN", Address: "o.local:50052", Probe: ProbeOK, AgentVersion: "0.10.4", OSVersion: "WendyOS-0.10.4"},
	}

	_, rows := PickerDeviceTableData(items, "", false)
	if len(rows) != 3 {
		t.Fatalf("rows = %d; want 3", len(rows))
	}
	for i, row := range rows {
		if len(row) < 5 {
			t.Fatalf("row %d has %d cells; want >= 5 (Name,Type,Address,Agent,OS)", i, len(row))
		}
	}

	// Agent cell is index 3, OS cell index 4 (no marker/default column here).
	if rows[0][3] != frame || rows[0][4] != frame {
		t.Errorf("pending row agent/os = %q/%q; want spinner frame %q", rows[0][3], rows[0][4], frame)
	}
	if rows[1][3] != ProbeFailedGlyph || rows[1][4] != ProbeFailedGlyph {
		t.Errorf("failed row agent/os = %q/%q; want %q", rows[1][3], rows[1][4], ProbeFailedGlyph)
	}
	if rows[2][3] != "0.10.4" || rows[2][4] != "WendyOS-0.10.4" {
		t.Errorf("ok row agent/os = %q/%q; want versions", rows[2][3], rows[2][4])
	}
}
