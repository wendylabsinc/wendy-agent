package commands

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

func outdatedLANModel(t *testing.T) discoverModel {
	t.Helper()
	m := newDiscoverModel(context.Background(), defaultOpts(), false)
	m.collection.LANDevices = []models.LANDevice{{
		DisplayName:  "Tom Thor Mac",
		Hostname:     "wendyos-tom-thor-mac.local",
		IPAddress:    "10.1.1.99",
		Port:         defaultAgentPort,
		AgentVersion: "2026.07.27-003050",
	}}
	m.refreshTable()
	m.table.SetCursor(0)
	return m
}

// Pressing "u" must not decide the outcome from the CLI's own version: whether
// an update is needed is a question about the release channel, answered in the
// worker. WDY-2039(c) — a dev CLI always claimed the device was up to date.
func TestDiscoverModel_UpdateKeyDefersToTheReleaseChannel(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })

	for _, cliVer := range []string{"dev", "2026.07.28-225011-dev", "2026.07.27-003050", "2026.07.29-120000"} {
		version.Version = cliVer
		m := outdatedLANModel(t)

		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
		dm := updated.(discoverModel)

		if cmd == nil {
			t.Errorf("cli %s: no update command started", cliVer)
		}
		if dm.updatingDeviceName != "Tom Thor Mac" {
			t.Errorf("cli %s: updatingDeviceName = %q, want the device", cliVer, dm.updatingDeviceName)
		}
		if strings.Contains(dm.flashMessage, "up to date") {
			t.Errorf("cli %s: refused before checking the channel: %q", cliVer, dm.flashMessage)
		}
		if want := "Checking Tom Thor Mac..."; dm.flashMessage != want {
			t.Errorf("cli %s: flash = %q, want %q", cliVer, dm.flashMessage, want)
		}
	}
}

func TestAgentAlreadyAtReleaseNote(t *testing.T) {
	tests := []struct {
		name             string
		agent, release   string
		wantNote         bool
		wantVersionInMsg string
	}{
		{"agent behind the release", "2026.07.27-003050", "2026.07.28-225023", false, ""},
		{"agent at the release", "2026.07.27-003050", "2026.07.27-003050", true, "2026.07.27-003050"},
		{"agent ahead of the release", "2026.07.28-225023", "2026.07.27-003050", true, "2026.07.28-225023"},
		// An explicit update replaces a dev agent with the release, matching
		// `wendy device update`.
		{"dev agent", "dev", "2026.07.27-003050", false, ""},
		{"branch-build agent", "2026.07.28-225011-dev", "2026.07.27-003050", false, ""},
		{"unknown agent version", "", "2026.07.27-003050", false, ""},
		{"unresolved release", "2026.07.27-003050", "", false, ""},
	}

	for _, tt := range tests {
		note := agentAlreadyAtReleaseNote("Tom Thor Mac", tt.agent, tt.release)
		if got := note != ""; got != tt.wantNote {
			t.Errorf("%s: note=%q, wantNote=%v", tt.name, note, tt.wantNote)
		}
		if tt.wantNote {
			if !strings.Contains(note, tt.wantVersionInMsg) {
				t.Errorf("%s: note %q does not name version %q", tt.name, note, tt.wantVersionInMsg)
			}
			// "latest release" is the honest claim: the TUI resolves the stable
			// channel, so a newer nightly may still exist.
			if !strings.Contains(note, "latest release") {
				t.Errorf("%s: note %q should say which channel it checked", tt.name, note)
			}
		}
	}
}

// The already-current outcome is good news, not a failure.
func TestDiscoverUpdateFlash(t *testing.T) {
	msg := discoverUpdateDoneMsg{deviceName: "Tom Thor Mac", note: "Tom Thor Mac is already at the latest release (2026.07.27-003050)."}
	got, isErr := discoverUpdateFlash(msg)
	if isErr {
		t.Errorf("already-current outcome rendered as an error: %q", got)
	}
	if got != msg.note {
		t.Errorf("flash = %q, want the note verbatim", got)
	}

	if got, isErr := discoverUpdateFlash(discoverUpdateDoneMsg{deviceName: "alpha"}); isErr || !strings.Contains(got, "successfully") {
		t.Errorf("success flash = %q (isError=%v)", got, isErr)
	}
	if got, isErr := discoverUpdateFlash(discoverUpdateDoneMsg{deviceName: "alpha", err: context.Canceled}); !isErr || !strings.Contains(got, "failed") {
		t.Errorf("error flash = %q (isError=%v)", got, isErr)
	}
}

// The discover table marks a stale agent whenever the comparison is meaningful,
// and the legend follows the rows.
func TestDiscoverTableItems_MarksOutdatedAgent(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "2026.07.29-120000"

	collection := &models.DevicesCollection{LANDevices: []models.LANDevice{{
		DisplayName:  "Tom Thor Mac",
		IPAddress:    "10.1.1.99",
		Port:         defaultAgentPort,
		AgentVersion: "2026.07.27-003050",
	}}}

	items := discoverTableItems(collection)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if !items[0].picker.AgentOutdated {
		t.Fatal("expected the agent to be marked outdated")
	}
	// Version text stays clean; the glyph is the table's business.
	if got := items[0].picker.AgentVersion; got != "2026.07.27-003050" {
		t.Errorf("AgentVersion = %q, want the bare version", got)
	}

	output := renderDeviceTable(collection)
	if !strings.Contains(output, tui.GlyphOutdated) || !strings.Contains(output, tui.LegendOutdated) {
		t.Errorf("expected a marked row and a matching legend, got %q", output)
	}
}
