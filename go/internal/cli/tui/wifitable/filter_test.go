package wifitable

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func filterFixture() []Network {
	return []Network{
		{SSID: "HomeNet", Known: true, Priority: 5, Signal: 80},
		{SSID: "my home 5G", Signal: 70, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_WPA2_PSK},
		{SSID: "CafeSpot", Signal: 55, Security: agentpb.WiFiSecurityType_WIFI_SECURITY_TYPE_OPEN},
	}
}

func typeRunes(m Model, s string) Model {
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
	}
	return m
}

func TestTypingEntersFilterModeAndNarrows(t *testing.T) {
	m := NewModel(filterFixture())

	m = typeRunes(m, "home") // 'h' is table-nav; seed with... use a non-nav rune first
	// 'h' is reserved for horizontal scroll, so the seed comes from 'o'.
	if m.mode != modeFiltering {
		t.Fatalf("typing should enter filter mode, got mode=%v", m.mode)
	}
	// Query is "ome" (h swallowed by scroll). Both home networks match.
	if got := m.filterInput.Value(); got != "ome" {
		t.Fatalf("filter query = %q, want %q", got, "ome")
	}
	visible := m.filteredNetworks()
	if len(visible) != 2 {
		t.Fatalf("expected 2 matches, got %d: %v", len(visible), ssids(visible))
	}
}

func TestSlashEntersFilterModeAndMatchesCaseInsensitively(t *testing.T) {
	m := NewModel(filterFixture())
	m = sendKey(m, "/")
	if m.mode != modeFiltering {
		t.Fatalf("/ should enter filter mode, got %v", m.mode)
	}
	m = typeRunes(m, "HOME")
	visible := m.filteredNetworks()
	if len(visible) != 2 {
		t.Fatalf("expected case-insensitive matches for HOME, got %v", ssids(visible))
	}
	for _, n := range visible {
		if !strings.Contains(strings.ToLower(n.SSID), "home") {
			t.Errorf("unexpected network in filtered view: %q", n.SSID)
		}
	}
}

func TestFilterEnterConnectsToFilteredSelection(t *testing.T) {
	h := &fakeHandler{}
	m := NewModel(filterFixture()).WithHandler(h)

	m = typeRunes(m, "cafe")
	if m.mode != modeFiltering {
		t.Fatalf("expected filter mode, got %v", m.mode)
	}
	if got := len(m.filteredNetworks()); got != 1 {
		t.Fatalf("expected 1 match for cafe, got %d", got)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	// CafeSpot is open → direct connect, no password prompt.
	if h.connectCalls != 1 || h.lastSSID != "CafeSpot" {
		t.Fatalf("expected connect to CafeSpot, got calls=%d ssid=%q", h.connectCalls, h.lastSSID)
	}
	if got := m.filterInput.Value(); got != "" {
		t.Errorf("filter should clear after connect, got %q", got)
	}
}

func TestFilterEnterOnSecuredNetworkPromptsForPassword(t *testing.T) {
	h := &fakeHandler{}
	m := NewModel(filterFixture()).WithHandler(h)

	m = typeRunes(m, "my")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)

	if m.mode != modePassword {
		t.Fatalf("secured unknown network must prompt for password, got mode=%v", m.mode)
	}
	if m.pwFor != "my home 5G" {
		t.Errorf("password prompt is for %q, want %q", m.pwFor, "my home 5G")
	}
	if h.connectCalls != 0 {
		t.Errorf("connect must not fire before the password is entered")
	}
}

func TestFilterEscRestoresFullListAndCursor(t *testing.T) {
	m := NewModel(filterFixture())
	m = typeRunes(m, "cafe")
	if got := len(m.filteredNetworks()); got != 1 {
		t.Fatalf("expected 1 match, got %d", got)
	}

	m = sendKey(m, "esc")
	if m.mode != modeBrowsing {
		t.Fatalf("esc should return to browsing, got %v", m.mode)
	}
	if got := len(m.filteredNetworks()); got != 3 {
		t.Fatalf("filter should be cleared, got %d visible", got)
	}
	if idx := m.table.Cursor(); idx < 0 || idx >= len(m.networks) || m.networks[idx].SSID != "CafeSpot" {
		t.Errorf("cursor should stay on CafeSpot after esc, got idx=%d", idx)
	}
}

func TestFilterBackspaceOnEmptyExitsFilterMode(t *testing.T) {
	m := NewModel(filterFixture())
	m = typeRunes(m, "x")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.mode != modeFiltering {
		t.Fatalf("backspace with a query should stay in filter mode")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = next.(Model)
	if m.mode != modeBrowsing {
		t.Fatalf("backspace on empty query should exit filter mode, got %v", m.mode)
	}
}

func TestNavKeysDoNotEnterFilterMode(t *testing.T) {
	m := NewModel(filterFixture())
	for _, k := range []string{"j", "k", "h", "l"} {
		m = sendKey(m, k)
		if m.mode != modeBrowsing {
			t.Fatalf("%q must stay table navigation, got mode=%v", k, m.mode)
		}
	}
}

func TestRefreshAppliesWhileFiltering(t *testing.T) {
	m := NewModel(filterFixture())
	m = typeRunes(m, "ome")

	next, _ := m.Update(RefreshMsg{Networks: []Network{
		{SSID: "HomeNet", Known: true, Signal: 81},
		{SSID: "my home 5G", Signal: 71},
		{SSID: "OtherHome", Signal: 30},
		{SSID: "CafeSpot", Signal: 55},
	}})
	m = next.(Model)

	if m.mode != modeFiltering {
		t.Fatalf("refresh must not kick the user out of filter mode")
	}
	if got := len(m.filteredNetworks()); got != 3 {
		t.Errorf("filtered view should track refreshed networks, got %d", got)
	}
	if got := len(m.networks); got != 4 {
		t.Errorf("full list should be replaced by refresh, got %d", got)
	}
}

func TestScanningPlaceholderUntilFirstRefresh(t *testing.T) {
	m := NewModel(nil)
	if view := m.View(); !strings.Contains(view, "Scanning for nearby networks") {
		t.Fatalf("empty model should show the scanning placeholder, got %q", view)
	}

	next, _ := m.Update(RefreshMsg{Networks: nil})
	m = next.(Model)
	if view := m.View(); !strings.Contains(view, "No networks found") {
		t.Errorf("after an empty refresh the placeholder should say no networks, got %q", view)
	}
}

func TestRefreshTickPollsHandlerOnlyWhenIdle(t *testing.T) {
	h := &fakeHandler{}
	m := NewModel(nil).WithHandler(h)

	// WithHandler marks the Init refresh as in flight: the first tick must
	// not stack another refresh.
	next, _ := m.Update(refreshTickMsg{})
	m = next.(Model)
	if h.refreshCalls != 0 {
		t.Fatalf("tick should skip refresh while one is in flight, got %d calls", h.refreshCalls)
	}

	// A RefreshMsg lands → next tick may poll again.
	next, _ = m.Update(RefreshMsg{Networks: filterFixture()})
	m = next.(Model)
	next, _ = m.Update(refreshTickMsg{})
	m = next.(Model)
	if h.refreshCalls != 1 {
		t.Fatalf("tick should poll once idle, got %d calls", h.refreshCalls)
	}

	// While that poll is outstanding, further ticks stay quiet.
	next, _ = m.Update(refreshTickMsg{})
	m = next.(Model)
	if h.refreshCalls != 1 {
		t.Errorf("tick must not stack refreshes, got %d calls", h.refreshCalls)
	}
}

func TestRefreshTickSkipsWhileRanking(t *testing.T) {
	h := &fakeHandler{}
	m := NewModel(filterFixture()).WithHandler(h)
	next, _ := m.Update(RefreshMsg{Networks: filterFixture()}) // clear in-flight
	m = next.(Model)

	m = sendKey(m, "r")
	if m.mode != modeRanking {
		t.Fatalf("expected rank mode, got %v", m.mode)
	}
	next, _ = m.Update(refreshTickMsg{})
	m = next.(Model)
	if h.refreshCalls != 0 {
		t.Errorf("tick should not poll while ranking, got %d calls", h.refreshCalls)
	}
}
