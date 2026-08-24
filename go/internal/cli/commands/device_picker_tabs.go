package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

type devicePickerTab int

const (
	devicePickerLocalTab devicePickerTab = iota
	devicePickerCloudTab
)

type devicePickerAction int

const (
	devicePickerNoAction devicePickerAction = iota
	devicePickerLogin
	devicePickerSwitchOrg
)

// Child messages are tagged so background commands keep updating the correct
// page after the user changes tabs.
type devicePickerLocalMsg struct{ msg tea.Msg }
type devicePickerCloudMsg struct{ msg tea.Msg }
type devicePickerOrgMsg struct{ name string }

type devicePickerModel struct {
	local        tui.PickerModel
	cloud        cloudDiscoverModel
	cloudAuth    *config.AuthConfig
	cloudOrg     string
	cloudStarted bool
	defaultOrg   int32
	active       devicePickerTab
	action       devicePickerAction
	cancelled    bool
	windowWidth  int
}

func newDevicePickerModel(ctx context.Context, local tui.PickerModel, auth *config.AuthConfig, defaultOrg int32) devicePickerModel {
	m := devicePickerModel{
		local:      local,
		cloudAuth:  auth,
		defaultOrg: defaultOrg,
	}
	if auth != nil {
		m.cloud = newCloudDiscoverModel(ctx, auth, os.Getenv("WENDY_BROKER_URL"), false, true, nil)
	}
	return m
}

func tagDevicePickerCmd(cmd tea.Cmd, tab devicePickerTab) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			tagged := make(tea.BatchMsg, 0, len(batch))
			for _, child := range batch {
				tagged = append(tagged, tagDevicePickerCmd(child, tab))
			}
			return tagged
		}
		if tab == devicePickerCloudTab {
			return devicePickerCloudMsg{msg: msg}
		}
		return devicePickerLocalMsg{msg: msg}
	}
}

func (m devicePickerModel) Init() tea.Cmd {
	return tagDevicePickerCmd(m.local.Init(), devicePickerLocalTab)
}

func (m devicePickerModel) startCloudCmd() tea.Cmd {
	if m.cloudAuth == nil {
		return nil
	}
	return tea.Batch(
		tagDevicePickerCmd(m.cloud.Init(), devicePickerCloudTab),
		m.loadOrgNameCmd(),
	)
}

func (m devicePickerModel) loadOrgNameCmd() tea.Cmd {
	ctx := m.cloud.ctx
	auth := m.cloudAuth
	orgID := cloudAuthOrgID(auth)
	return func() tea.Msg {
		orgs, err := listOrgsFromCloud(ctx, auth)
		if err != nil {
			return devicePickerOrgMsg{}
		}
		for _, org := range orgs {
			if org.GetId() == orgID {
				return devicePickerOrgMsg{name: org.GetName()}
			}
		}
		return devicePickerOrgMsg{}
	}
}

func (m devicePickerModel) updateLocal(msg tea.Msg) (devicePickerModel, tea.Cmd) {
	updated, cmd := m.local.Update(msg)
	m.local = updated.(tui.PickerModel)
	if m.local.Selected() != nil {
		return m, tea.Quit
	}
	if m.local.Cancelled() {
		m.cancelled = true
		return m, tea.Quit
	}
	return m, tagDevicePickerCmd(cmd, devicePickerLocalTab)
}

func (m devicePickerModel) updateCloud(msg tea.Msg) (devicePickerModel, tea.Cmd) {
	updated, cmd := m.cloud.Update(msg)
	m.cloud = updated.(cloudDiscoverModel)
	if m.cloud.selected != nil {
		return m, tea.Quit
	}
	if m.cloud.quitting {
		m.cancelled = true
		return m, tea.Quit
	}
	return m, tagDevicePickerCmd(cmd, devicePickerCloudTab)
}

func (m devicePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case devicePickerLocalMsg:
		return m.updateLocal(msg.msg)
	case devicePickerCloudMsg:
		if m.cloudAuth == nil {
			return m, nil
		}
		return m.updateCloud(msg.msg)
	case devicePickerOrgMsg:
		m.cloudOrg = msg.name
		return m, nil
	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		local, localCmd := m.updateLocal(msg)
		m = local
		if m.cloudAuth == nil {
			return m, localCmd
		}
		cloud, cloudCmd := m.updateCloud(msg)
		m = cloud
		return m, tea.Batch(localCmd, cloudCmd)
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "shift+tab":
			if m.active == devicePickerLocalTab {
				m.active = devicePickerCloudTab
				if m.cloudAuth != nil && !m.cloudStarted {
					m.cloudStarted = true
					return m, m.startCloudCmd()
				}
			} else {
				m.active = devicePickerLocalTab
			}
			return m, nil
		case "o":
			if m.active == devicePickerCloudTab && m.cloudAuth != nil {
				m.action = devicePickerSwitchOrg
				return m, tea.Quit
			}
		case "enter":
			if m.active == devicePickerCloudTab && m.cloudAuth == nil {
				m.action = devicePickerLogin
				return m, tea.Quit
			}
		case "q", "esc", "ctrl+c":
			if m.active == devicePickerCloudTab && m.cloudAuth == nil {
				m.cancelled = true
				return m, tea.Quit
			}
		}

		if m.active == devicePickerCloudTab {
			if m.cloudAuth == nil {
				return m, nil
			}
			return m.updateCloud(msg)
		}
		return m.updateLocal(msg)
	}
	return m, nil
}

var (
	devicePickerTabActive   = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(tui.ColorPrimary)
	devicePickerTabInactive = lipgloss.NewStyle().Foreground(tui.ColorDim)
	devicePickerOrgStyle    = lipgloss.NewStyle().Foreground(tui.ColorDim)
)

func (m devicePickerModel) View() string {
	if m.cancelled || m.action != devicePickerNoAction || m.selectedLocal() != nil || m.selectedCloud() != nil {
		return ""
	}

	header := deviceTabsHeader(m.active, m.windowWidth)

	if m.active == devicePickerLocalTab {
		return header + "\n\n" + m.local.View()
	}

	var body strings.Builder
	body.WriteString(header)
	body.WriteString("\n\n")
	if m.cloudAuth == nil {
		body.WriteString("Select a cloud device\n")
		body.WriteString(devicePickerOrgStyle.Render("  ☐  Wendy Cloud login   Not logged in"))
		body.WriteString("\n")
		body.WriteString(devicePickerOrgStyle.Render("  enter log in, tab local, q quit"))
		body.WriteString("\n")
		return body.String()
	}

	body.WriteString(devicePickerOrgStyle.Render(deviceCloudOrgLabel(m.cloudAuth, m.cloudOrg, m.defaultOrg)))
	body.WriteString("\n\n")
	body.WriteString(m.cloud.View())
	return body.String()
}

func deviceTabsHeader(active devicePickerTab, width int) string {
	local := devicePickerTabInactive.Render("Local")
	cloud := devicePickerTabInactive.Render("Cloud")
	if active == devicePickerLocalTab {
		local = devicePickerTabActive.Render("Local")
	} else {
		cloud = devicePickerTabActive.Render("Cloud")
	}
	header := local + devicePickerTabInactive.Render(" | ") + cloud + devicePickerTabInactive.Render("  (tab switch)")
	if width > 0 {
		header = tui.CropANSIView(header, 0, width)
	}
	return header
}

func deviceCloudOrgLabel(auth *config.AuthConfig, name string, defaultOrg int32) string {
	orgID := cloudAuthOrgID(auth)
	label := fmt.Sprintf("Organization: org %d", orgID)
	if name != "" {
		label = fmt.Sprintf("Organization: %s (org %d)", name, orgID)
	}
	if orgID != 0 && orgID == defaultOrg {
		label += "  ✦ default"
	}
	return label + "  (o switch)"
}

func (m devicePickerModel) selectedLocal() *tui.PickerItem {
	return m.local.Selected()
}

func (m devicePickerModel) selectedCloud() *cloudpb.Asset {
	if m.cloudAuth == nil {
		return nil
	}
	return m.cloud.selected
}

func cloudAuthOrgID(auth *config.AuthConfig) int32 {
	if auth == nil || len(auth.Certificates) == 0 {
		return 0
	}
	return int32(auth.Certificates[0].OrganizationID)
}

// devicePickerInitialAuth chooses the persisted default session when possible.
// If the user has authenticated sessions but no default yet, the first valid
// session is still useful as the initial cloud page; the 'o' hotkey lets them
// switch and mark another session as default.
func devicePickerInitialAuth(cfg *config.Config) *config.AuthConfig {
	if cfg == nil || len(cfg.Auth) == 0 {
		return nil
	}
	if auth, err := config.ResolveAuth(cfg, "", nil); err == nil {
		return auth
	}
	for i := range cfg.Auth {
		if len(cfg.Auth[i].Certificates) > 0 {
			return &cfg.Auth[i]
		}
	}
	return nil
}
