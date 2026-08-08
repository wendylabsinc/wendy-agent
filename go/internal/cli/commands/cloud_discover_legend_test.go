package commands

import (
	"strings"
	"testing"

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
