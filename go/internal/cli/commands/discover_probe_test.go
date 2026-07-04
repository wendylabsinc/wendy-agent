package commands

import (
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// discoverAgentCell returns the Agent-column cell of the single table row.
func discoverAgentCell(t *testing.T, m discoverModel) string {
	t.Helper()
	agentIdx := -1
	for i, c := range m.table.Columns() {
		if c.Title == "Agent" {
			agentIdx = i
		}
	}
	if agentIdx < 0 {
		t.Fatalf("no Agent column; columns=%v", m.table.Columns())
	}
	rows := m.table.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	return rows[0][agentIdx]
}

func TestDiscoverModel_LANProbePendingThenOK(t *testing.T) {
	m := newDiscoverModel(context.Background(), defaultOpts(), true)

	// A LAN device discovered without a version is "connecting": probe state is
	// pending and the Agent cell shows a (non-empty) spinner frame, not blank.
	updated, _ := m.Update(lanScanMsg{devices: []models.LANDevice{{DisplayName: "alpha"}}})
	dm := updated.(discoverModel)
	if dm.probe["alpha"] != tui.ProbePending {
		t.Fatalf("probe state = %v; want pending", dm.probe["alpha"])
	}
	if cell := discoverAgentCell(t, dm); cell == "" {
		t.Fatal("pending device Agent cell should show a spinner frame, got blank")
	}

	// The probe resolves: the version appears and the state becomes OK.
	updated, _ = dm.Update(lanProbeMsg{name: "alpha", dev: models.LANDevice{
		DisplayName: "alpha", AgentVersion: "0.10.4", OSVersion: "WendyOS-0.10.4",
	}})
	dm = updated.(discoverModel)
	if dm.probe["alpha"] != tui.ProbeOK {
		t.Fatalf("probe state = %v; want ok", dm.probe["alpha"])
	}
	if cell := discoverAgentCell(t, dm); cell != "0.10.4" {
		t.Fatalf("resolved Agent cell = %q; want 0.10.4", cell)
	}
}

func TestDiscoverModel_LANProbeFailedShowsGlyph(t *testing.T) {
	m := newDiscoverModel(context.Background(), defaultOpts(), true)
	updated, _ := m.Update(lanScanMsg{devices: []models.LANDevice{{DisplayName: "beta"}}})
	dm := updated.(discoverModel)

	updated, _ = dm.Update(lanProbeMsg{name: "beta", err: context.DeadlineExceeded})
	dm = updated.(discoverModel)
	if dm.probe["beta"] != tui.ProbeFailed {
		t.Fatalf("probe state = %v; want failed", dm.probe["beta"])
	}
	if cell := discoverAgentCell(t, dm); cell != tui.ProbeFailedGlyph {
		t.Fatalf("failed Agent cell = %q; want %q", cell, tui.ProbeFailedGlyph)
	}
}

func TestDiscoverModel_LANProbeOKSurvivesTransientFailure(t *testing.T) {
	m := newDiscoverModel(context.Background(), defaultOpts(), true)
	updated, _ := m.Update(lanScanMsg{devices: []models.LANDevice{{DisplayName: "gamma"}}})
	dm := updated.(discoverModel)
	updated, _ = dm.Update(lanProbeMsg{name: "gamma", dev: models.LANDevice{DisplayName: "gamma", AgentVersion: "1.2.3"}})
	dm = updated.(discoverModel)

	// A later failed probe must not erase a known version.
	updated, _ = dm.Update(lanProbeMsg{name: "gamma", err: context.DeadlineExceeded})
	dm = updated.(discoverModel)
	if dm.probe["gamma"] != tui.ProbeOK {
		t.Fatalf("probe state = %v; want ok (sticky)", dm.probe["gamma"])
	}
	if cell := discoverAgentCell(t, dm); cell != "1.2.3" {
		t.Fatalf("Agent cell = %q; want 1.2.3 retained", cell)
	}
}
