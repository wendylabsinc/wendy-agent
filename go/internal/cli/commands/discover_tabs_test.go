package commands

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

func newTestDiscoverTabsModel(authOrg int, defaultOrg int32) discoverTabsModel {
	ctx := context.Background()
	local := newDiscoverModel(ctx, defaultOpts(), true)
	if authOrg == 0 {
		return newDiscoverTabsModel(ctx, local, nil, defaultOrg)
	}
	return newDiscoverTabsModel(ctx, local, pickerAuth(authOrg), defaultOrg)
}

func TestDiscoverTabsShowsLocalAndCloud(t *testing.T) {
	m := newTestDiscoverTabsModel(0, 0)
	view := m.View()
	for _, want := range []string{"Local", "Cloud", "tab switch", "Scanning for WendyOS devices"} {
		if !strings.Contains(view, want) {
			t.Fatalf("local discover view does not contain %q: %q", want, view)
		}
	}
}

func TestDiscoverTabsLoggedOutCloudOffersLogin(t *testing.T) {
	m := newTestDiscoverTabsModel(0, 0)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(discoverTabsModel)

	view := m.View()
	for _, want := range []string{"Discover cloud devices", "Wendy Cloud login", "Not logged in"} {
		if !strings.Contains(view, want) {
			t.Fatalf("logged-out cloud discover view does not contain %q: %q", want, view)
		}
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(discoverTabsModel)
	if cmd == nil || m.action != devicePickerLogin {
		t.Fatalf("login row result: cmd=%v action=%v", cmd != nil, m.action)
	}
}

func TestDiscoverTabsStartsCloudLazilyAndShowsDefaultOrg(t *testing.T) {
	m := newTestDiscoverTabsModel(7, 7)
	if m.cloudStarted {
		t.Fatal("cloud discovery started before visiting the Cloud tab")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(discoverTabsModel)
	if cmd == nil || !m.cloudStarted {
		t.Fatalf("first Cloud visit: cmd=%v started=%v", cmd != nil, m.cloudStarted)
	}
	updated, _ = m.Update(discoverTabsOrgMsg{name: "Robotics"})
	m = updated.(discoverTabsModel)
	for _, want := range []string{"Organization: Robotics (org 7)", "default", "o switch"} {
		if !strings.Contains(m.View(), want) {
			t.Fatalf("cloud discover view does not contain %q: %q", want, m.View())
		}
	}
}

func TestDiscoverTabsOrgSwitchHotkey(t *testing.T) {
	m := newTestDiscoverTabsModel(7, 7)
	m.active = devicePickerCloudTab

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	m = updated.(discoverTabsModel)
	if cmd == nil || m.action != devicePickerSwitchOrg {
		t.Fatalf("org switch result: cmd=%v action=%v", cmd != nil, m.action)
	}
}

func TestDiscoverTabsCloudRosterUsesCloudDiscoverDashboard(t *testing.T) {
	m := newTestDiscoverTabsModel(7, 7)
	m.active = devicePickerCloudTab
	asset := &cloudpb.Asset{Id: 42, Name: "cloud-pi"}

	updated, _ := m.Update(discoverTabsCloudMsg{msg: cloudScanMsg{assets: []*cloudpb.Asset{asset}}})
	m = updated.(discoverTabsModel)
	view := m.View()
	for _, want := range []string{"cloud-pi", "enter copy", "a copy all", "u update"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cloud roster does not contain %q: %q", want, view)
		}
	}
}

func TestTagDiscoverTabsCmdPreservesBatchChildren(t *testing.T) {
	cmd := tagDiscoverTabsCmd(tea.Batch(
		func() tea.Msg { return "first" },
		func() tea.Msg { return "second" },
	), devicePickerLocalTab)
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("tagged batch returned unexpected message type")
	}
	if len(batch) != 2 {
		t.Fatalf("tagged batch children = %d, want 2", len(batch))
	}
	for i, child := range batch {
		msg, ok := child().(discoverTabsLocalMsg)
		if !ok {
			t.Fatalf("child %d returned unexpected message type", i)
		}
		if msg.msg == nil {
			t.Fatalf("child %d lost its payload", i)
		}
	}
}
