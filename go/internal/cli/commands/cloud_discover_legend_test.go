package commands

import (
	"strings"
	"testing"

	bubbleTable "github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// The cloud table marks staleness with the same glyph as the LAN table, and
// documents it only when a row carries it. It has no provisioned/default
// markers, so the base legend must not appear.
func TestCloudDiscoverLegendMatchesRows(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })

	assets := []*cloudpb.Asset{{Id: 1, Name: "Tom Thor Mac"}}
	versions := map[int32]*agentpb.GetAgentVersionResponse{
		1: {Version: "2026.07.27-003050"},
	}

	for _, tt := range []struct {
		cliVer      string
		wantMarked  bool
		description string
	}{
		{"2026.07.29-120000", true, "CLI ahead of the agent"},
		{"2026.07.27-003050", false, "matched pair"},
		{"dev", false, "dev CLI has no comparable version"},
	} {
		version.Version = tt.cliVer

		rows := cloudDiscoverTableRows(assets, versions)
		marked := strings.Contains(strings.Join(rows[0], " "), tui.GlyphOutdated)
		legend := tui.DeviceWarningLegend(cloudLegendItems(assets, versions))

		if marked != tt.wantMarked {
			t.Errorf("%s: row marked=%v, want %v (%q)", tt.description, marked, tt.wantMarked, rows[0])
		}
		if documented := strings.Contains(legend, tui.LegendOutdated); documented != marked {
			t.Errorf("%s: row marked=%v but legend documents=%v (%q)", tt.description, marked, documented, legend)
		}
		if strings.Contains(legend, "provisioned") || strings.Contains(legend, "default") {
			t.Errorf("%s: legend %q documents markers the cloud table cannot render", tt.description, legend)
		}
	}
}

// An unreachable device has no version to compare, so nothing is marked and no
// legend line is printed.
func TestCloudDiscoverLegendEmptyWithoutVersions(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "2026.07.29-120000"

	assets := []*cloudpb.Asset{{Id: 1, Name: "offline"}}
	rows := cloudDiscoverTableRows(assets, nil)
	if got := strings.Join(rows[0], " "); strings.Contains(got, tui.GlyphOutdated) {
		t.Errorf("marked a row with no probe: %q", got)
	}
	if legend := tui.DeviceWarningLegend(cloudLegendItems(assets, nil)); legend != "" {
		t.Errorf("legend = %q, want none", legend)
	}
}

// The legend is derived from the version data, but the glyph it documents has
// to survive the table's column widths to be of any use. A release version is
// 17 cells and the glyph pushes the cell to 19, so a tight Version cap used to
// truncate both — printing "⚠ agent older than CLI" under a table where no row
// showed a ⚠, and hiding the last digits of every version besides.
func TestCloudDiscoverGlyphSurvivesRender(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "2026.08.23-120000"

	assets := []*cloudpb.Asset{
		{Id: 1, Name: "Spark3"},
		{Id: 2, Name: "wendy-arms"},
	}
	versions := map[int32]*agentpb.GetAgentVersionResponse{
		1: {Version: "2026.08.22-032058"},
		2: {Version: "dev"},
	}

	rows := cloudDiscoverTableRows(assets, versions)
	cols := discoverTableColumns(rows)
	table := newDiscoverTable(true)
	table.SetColumns(cols)
	table.SetRows(rows)
	table.SetWidth(discoverTableWidth(cols))
	table.SetHeight(discoverTableHeight(len(rows), 40, true))
	view := table.View()

	legend := tui.DeviceWarningLegend(cloudLegendItems(assets, versions))
	if !strings.Contains(legend, tui.LegendOutdated) {
		t.Fatalf("legend = %q, want it to document %q", legend, tui.LegendOutdated)
	}
	if !strings.Contains(view, tui.GlyphOutdated) {
		t.Errorf("legend documents %q but the rendered table shows no glyph:\n%s", tui.LegendOutdated, view)
	}
	if !strings.Contains(view, "2026.08.22-032058") {
		t.Errorf("release version rendered truncated:\n%s", view)
	}
}

// The Version column holds a full release version even when nothing is flagged,
// so two builds from the same day stay distinguishable.
func TestCloudDiscoverVersionNotTruncated(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "dev" // a dev CLI flags nothing, so no glyph is appended

	assets := []*cloudpb.Asset{{Id: 1, Name: "Spark3"}}
	versions := map[int32]*agentpb.GetAgentVersionResponse{1: {Version: "2026.08.22-032058"}}

	rows := cloudDiscoverTableRows(assets, versions)
	cols := discoverTableColumns(rows)
	table := newDiscoverTable(false)
	table.SetColumns(cols)
	table.SetRows(rows)
	table.SetWidth(discoverTableWidth(cols))
	table.SetHeight(discoverTableHeight(len(rows), 40, false))

	if view := table.View(); !strings.Contains(view, "2026.08.22-032058") {
		t.Errorf("version truncated in the Version column:\n%s", view)
	}
}

// A marked cell overrides the Version max width, so tightening that constant
// can no longer silently eat the glyph.
func TestDiscoverTableColumnsFitWarningGlyph(t *testing.T) {
	rows := []bubbleTable.Row{{"", "Spark3", "Linux (arm64)", "2026.08.22-032058 " + tui.GlyphOutdated}}
	cols := discoverTableColumns(rows)
	if got, want := cols[3].Width, lipgloss.Width(rows[0][3]); got < want {
		t.Errorf("Version column width = %d, want >= %d so the glyph is not truncated", got, want)
	}
}
