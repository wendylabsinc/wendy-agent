package commands

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/discovery"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// lanCachedEvent builds a lanEventMsg for a device that just appeared from
// the on-disk cache (unverified this session). ch is left nil: none of these
// tests execute the returned re-arm command, only the state transition it
// produces.
func lanCachedEvent(dev models.LANDevice) lanEventMsg {
	return lanEventMsg{ev: discovery.LANEvent{Kind: discovery.LANCached, Device: dev}}
}

// lanFoundEvent builds a lanEventMsg for a live-confirmed, agent-probed
// sighting (Probed: true) — the common case in these tests, where the
// device's full metadata is already known.
func lanFoundEvent(dev models.LANDevice) lanEventMsg {
	return lanEventMsg{ev: discovery.LANEvent{Kind: discovery.LANFound, Device: dev, Probed: true}}
}

// lanOfflineEvent builds a lanEventMsg reporting dev as confirmed offline.
func lanOfflineEvent(dev models.LANDevice) lanEventMsg {
	return lanEventMsg{ev: discovery.LANEvent{Kind: discovery.LANOffline, Device: dev}}
}

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

// findLANEventMsg runs cmd and, if it produced a tea.BatchMsg (as
// discoverModel.Init does — the LAN stream cmd is batched alongside the
// spinner tick), searches the batch for the sub-command that yields a
// lanEventMsg. Other sub-commands (e.g. the spinner tick) are executed too
// but their messages are discarded.
func findLANEventMsg(t *testing.T, cmd tea.Cmd) lanEventMsg {
	t.Helper()
	msg, ok := searchLANEventMsg(cmd)
	if !ok {
		t.Fatalf("no lanEventMsg produced by cmd (got %#v)", msg)
	}
	return msg
}

func searchLANEventMsg(cmd tea.Cmd) (lanEventMsg, bool) {
	if cmd == nil {
		return lanEventMsg{}, false
	}
	switch v := cmd().(type) {
	case lanEventMsg:
		return v, true
	case tea.BatchMsg:
		for _, sub := range v {
			if m, ok := searchLANEventMsg(sub); ok {
				return m, true
			}
		}
	}
	return lanEventMsg{}, false
}

// TestDiscoverStream_CachedThenProbedUpdatesInPlace drives the discover
// model's real Init/Update wiring — a fake lanStreamFn feeds events through
// the channel Init hands to waitLANEvent — and verifies: a cached row shows
// up pending, a later probed sighting of the same identity updates that row
// in place rather than duplicating it, and going offline flips it to
// ProbeOffline while keeping it listed.
func TestDiscoverStream_CachedThenProbedUpdatesInPlace(t *testing.T) {
	orig := lanStreamFn
	t.Cleanup(func() { lanStreamFn = orig })

	ch := make(chan discovery.LANEvent, 8)
	lanStreamFn = func(ctx context.Context, opts discovery.StreamOptions) <-chan discovery.LANEvent {
		// The CRITICAL wiring rule (Task 3's design): with a nil Prober a
		// cached row can never be confirmed offline. Both surfaces that
		// consume StreamLAN must always pass a real prober.
		if opts.Prober == nil {
			t.Error("StreamOptions.Prober must be set — a nil Prober can never confirm a cached row offline")
		}
		if !opts.UseCache {
			t.Error("StreamOptions.UseCache must be true so cached rows appear instantly")
		}
		return ch
	}

	m := newDiscoverModel(context.Background(), discovery.DiscoveryOptions{Types: []models.InterfaceType{models.InterfaceLAN}}, true)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() returned a nil cmd")
	}

	dev := models.LANDevice{ID: "dev-1", DisplayName: "alpha", Hostname: "alpha.local", Port: defaultAgentPort}

	// Cached: the row appears immediately, pending — instant visibility is
	// the entire point of this task.
	ch <- discovery.LANEvent{Kind: discovery.LANCached, Device: dev}
	msg := findLANEventMsg(t, cmd)
	updated, nextCmd := m.Update(msg)
	dm := updated.(discoverModel)
	if got := len(dm.collection.LANDevices); got != 1 {
		t.Fatalf("LANDevices = %d after cached event; want 1", got)
	}
	if dm.probe["alpha"] != tui.ProbePending {
		t.Fatalf("probe state = %v; want ProbePending", dm.probe["alpha"])
	}
	if cell := discoverAgentCell(t, dm); cell == "" {
		t.Fatal("pending device Agent cell should show a spinner frame, got blank")
	}

	// Probed Found for the same identity: the row updates in place — no
	// duplicate — and the version appears.
	probedDev := dev
	probedDev.AgentVersion = "0.10.4"
	probedDev.OSVersion = "WendyOS-0.10.4"
	ch <- discovery.LANEvent{Kind: discovery.LANFound, Device: probedDev, Probed: true}
	msg = findLANEventMsg(t, nextCmd)
	updated, nextCmd = dm.Update(msg)
	dm = updated.(discoverModel)
	if got := len(dm.collection.LANDevices); got != 1 {
		t.Fatalf("LANDevices = %d after probed update; want 1 (no duplicate row)", got)
	}
	if dm.probe["alpha"] != tui.ProbeOK {
		t.Fatalf("probe state = %v; want ProbeOK", dm.probe["alpha"])
	}
	if cell := discoverAgentCell(t, dm); cell != "0.10.4" {
		t.Fatalf("resolved Agent cell = %q; want 0.10.4", cell)
	}

	// Offline: the row stays listed (never removed) but flips to
	// ProbeOffline.
	ch <- discovery.LANEvent{Kind: discovery.LANOffline, Device: probedDev}
	msg = findLANEventMsg(t, nextCmd)
	updated, _ = dm.Update(msg)
	dm = updated.(discoverModel)
	if got := len(dm.collection.LANDevices); got != 1 {
		t.Fatalf("LANDevices = %d after offline event; want 1 (row stays listed)", got)
	}
	if dm.probe["alpha"] != tui.ProbeOffline {
		t.Fatalf("probe state = %v; want ProbeOffline", dm.probe["alpha"])
	}
	if cell := discoverAgentCell(t, dm); cell != "offline" {
		t.Fatalf("offline Agent cell = %q; want %q", cell, "offline")
	}
}

// TestDiscoverStream_UpdatedEventAlsoUpdatesInPlace checks the LANUpdated
// event kind (an address change or a re-probe of an already-emitted device)
// merges into the same row rather than appending a second one.
func TestDiscoverStream_UpdatedEventAlsoUpdatesInPlace(t *testing.T) {
	m := newDiscoverModel(context.Background(), defaultOpts(), true)

	dev := models.LANDevice{ID: "dev-2", DisplayName: "gamma", IPAddress: "10.0.0.5", Port: defaultAgentPort}
	updated, _ := m.Update(lanFoundEvent(dev))
	dm := updated.(discoverModel)
	if got := len(dm.collection.LANDevices); got != 1 {
		t.Fatalf("LANDevices = %d after found event; want 1", got)
	}

	moved := dev
	moved.IPAddress = "10.0.0.9"
	updated, _ = dm.Update(lanEventMsg{ev: discovery.LANEvent{Kind: discovery.LANUpdated, Device: moved, Probed: true}})
	dm = updated.(discoverModel)
	if got := len(dm.collection.LANDevices); got != 1 {
		t.Fatalf("LANDevices = %d after updated event; want 1 (merged in place)", got)
	}
	if dm.collection.LANDevices[0].IPAddress != "10.0.0.9" {
		t.Fatalf("IPAddress = %q; want the updated address", dm.collection.LANDevices[0].IPAddress)
	}
	if dm.probe["gamma"] != tui.ProbeOK {
		t.Fatalf("probe state = %v; want ProbeOK", dm.probe["gamma"])
	}
}

// TestDiscoverStream_UnprobedFoundShowsPending checks a live mDNS sighting
// that has not yet been confirmed by an agent probe (Probed: false) renders
// as ProbePending, matching the "connecting" spinner the picker shows for
// the same case.
func TestDiscoverStream_UnprobedFoundShowsPending(t *testing.T) {
	m := newDiscoverModel(context.Background(), defaultOpts(), true)

	dev := models.LANDevice{ID: "dev-3", DisplayName: "delta", IPAddress: "10.0.0.6", Port: defaultAgentPort}
	updated, _ := m.Update(lanEventMsg{ev: discovery.LANEvent{Kind: discovery.LANFound, Device: dev, Probed: false}})
	dm := updated.(discoverModel)

	if dm.probe["delta"] != tui.ProbePending {
		t.Fatalf("probe state = %v; want ProbePending", dm.probe["delta"])
	}
	if cell := discoverAgentCell(t, dm); cell == "" {
		t.Fatal("unprobed device Agent cell should show a spinner frame, got blank")
	}
}
