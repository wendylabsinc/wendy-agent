package tui

import (
	"strings"
	"testing"
)

// The legend and the rows must agree: a warning glyph is documented exactly
// when some row renders it. WDY-2039(a) shipped a legend describing ⚠ as
// staleness while the only ⚠ rendered meant "no mTLS".
func TestDeviceTableLegend_DocumentsExactlyTheGlyphsRendered(t *testing.T) {
	stale := PickerItem{Name: "stale", AgentVersion: "2026.07.27-003050", AgentOutdated: true}
	insecure := PickerItem{Name: "insecure", AgentVersion: "2026.07.28-225023", Insecure: true}
	plain := PickerItem{Name: "plain", AgentVersion: "2026.07.28-225023"}

	tests := []struct {
		name  string
		items []PickerItem
	}{
		{"none", []PickerItem{plain}},
		{"outdated only", []PickerItem{plain, stale}},
		{"insecure only", []PickerItem{plain, insecure}},
		{"both", []PickerItem{stale, insecure}},
		{"empty", nil},
	}

	for _, tt := range tests {
		legend := DeviceTableLegend(tt.items)
		if !strings.Contains(legend, DeviceTableLegendBase) {
			t.Errorf("%s: legend %q is missing the always-documented glyphs", tt.name, legend)
		}
		_, rows := PickerDeviceTableData(tt.items, "", true)
		var rendered string
		for _, row := range rows {
			rendered += strings.Join(row, " ")
		}
		for glyph, entry := range map[string]string{GlyphOutdated: LegendOutdated, GlyphInsecure: LegendInsecure} {
			inRows := strings.Contains(rendered, glyph)
			inLegend := strings.Contains(legend, entry)
			if inRows != inLegend {
				t.Errorf("%s: glyph %q rendered=%v documented=%v (legend %q, rows %q)",
					tt.name, glyph, inRows, inLegend, legend, rendered)
			}
		}
	}
}

// A pending probe renders a spinner frame in the Agent cell, so there is
// nothing to mark and nothing to document.
func TestDeviceTableLegend_SkipsOutdatedWhileProbePending(t *testing.T) {
	items := []PickerItem{{
		Name:          "alpha",
		AgentVersion:  "2026.07.27-003050",
		AgentOutdated: true,
		Probe:         ProbePending,
		ProbeFrame:    "⣟",
	}}

	if legend := DeviceTableLegend(items); strings.Contains(legend, LegendOutdated) {
		t.Fatalf("legend documents staleness for a pending probe: %q", legend)
	}
	_, rows := PickerDeviceTableData(items, "", true)
	if got := strings.Join(rows[0], " "); strings.Contains(got, GlyphOutdated) {
		t.Fatalf("row marks staleness for a pending probe: %q", got)
	}
}

// The two warning states are distinct glyphs on distinct columns: staleness
// annotates the agent version, no-mTLS annotates the name.
func TestPickerDeviceTableData_MarksStalenessAndInsecureSeparately(t *testing.T) {
	cols, rows := PickerDeviceTableData([]PickerItem{{
		Name:          "Tom Thor Mac",
		Type:          "LAN",
		Address:       "wendyos-tom-thor-mac.local",
		AgentVersion:  "2026.07.27-003050",
		OSVersion:     "WendyOS-0.17.0",
		Provisioned:   "Unprovisioned",
		AgentOutdated: true,
		Insecure:      true,
	}}, "", true)

	cell := func(title string) string {
		for i, col := range cols {
			if col.Title == title {
				return rows[0][i]
			}
		}
		t.Fatalf("no %q column in %v", title, cols)
		return ""
	}

	if got, want := cell("Agent"), "2026.07.27-003050 "+GlyphOutdated; got != want {
		t.Errorf("Agent cell = %q, want %q", got, want)
	}
	if got, want := cell("Name"), "Tom Thor Mac "+GlyphInsecure; got != want {
		t.Errorf("Name cell = %q, want %q", got, want)
	}
}

// Tables without provisioned/default markers document only what they render,
// and omit the line entirely when nothing is marked.
func TestDeviceWarningLegend(t *testing.T) {
	if got := DeviceWarningLegend([]PickerItem{{AgentVersion: "2026.07.28-225023"}}); got != "" {
		t.Errorf("unmarked rows produced legend %q, want empty", got)
	}
	got := DeviceWarningLegend([]PickerItem{{AgentVersion: "2026.07.27-003050", AgentOutdated: true}})
	if got != LegendOutdated {
		t.Errorf("legend = %q, want %q", got, LegendOutdated)
	}
	if strings.Contains(got, "provisioned") {
		t.Errorf("legend %q documents markers this table cannot render", got)
	}
}

// An offline row renders "offline" in the Agent cell and its version is
// cached metadata no live probe has confirmed — the spec's trust boundary
// says update hints must never fire from that alone.
func TestDeviceTableLegend_SkipsOutdatedWhileOffline(t *testing.T) {
	items := []PickerItem{{
		Name:          "alpha",
		AgentVersion:  "2026.07.27-003050",
		AgentOutdated: true,
		Probe:         ProbeOffline,
	}}

	if legend := DeviceTableLegend(items); strings.Contains(legend, LegendOutdated) {
		t.Fatalf("legend documents staleness for an offline device: %q", legend)
	}
	_, rows := PickerDeviceTableData(items, "", true)
	if got := strings.Join(rows[0], " "); strings.Contains(got, GlyphOutdated) {
		t.Fatalf("row marks staleness for an offline device: %q", got)
	}
}
