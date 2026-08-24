package commands

import (
	"context"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

type discoverTabsLocalMsg struct{ msg tea.Msg }
type discoverTabsCloudMsg struct{ msg tea.Msg }
type discoverTabsOrgMsg struct{ name string }

type discoverTabsModel struct {
	local        discoverModel
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

func newDiscoverTabsModel(ctx context.Context, local discoverModel, auth *config.AuthConfig, defaultOrg int32) discoverTabsModel {
	m := discoverTabsModel{
		local:      local,
		cloudAuth:  auth,
		defaultOrg: defaultOrg,
	}
	if auth != nil {
		m.cloud = newCloudDiscoverModel(ctx, auth, os.Getenv("WENDY_BROKER_URL"), false, false, nil)
	}
	return m
}

// tagDiscoverTabsCmd preserves which dashboard owns an asynchronous result.
// discoverModel.Init returns tea.Batch, so BatchMsg children must be tagged
// individually rather than hiding the batch inside one wrapper message.
func tagDiscoverTabsCmd(cmd tea.Cmd, tab devicePickerTab) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			tagged := make(tea.BatchMsg, 0, len(batch))
			for _, child := range batch {
				tagged = append(tagged, tagDiscoverTabsCmd(child, tab))
			}
			return tagged
		}
		if tab == devicePickerCloudTab {
			return discoverTabsCloudMsg{msg: msg}
		}
		return discoverTabsLocalMsg{msg: msg}
	}
}

func (m discoverTabsModel) Init() tea.Cmd {
	return tagDiscoverTabsCmd(m.local.Init(), devicePickerLocalTab)
}

func (m discoverTabsModel) startCloudCmd() tea.Cmd {
	if m.cloudAuth == nil {
		return nil
	}
	return tea.Batch(
		tagDiscoverTabsCmd(m.cloud.Init(), devicePickerCloudTab),
		m.loadOrgNameCmd(),
	)
}

func (m discoverTabsModel) loadOrgNameCmd() tea.Cmd {
	ctx := m.cloud.ctx
	auth := m.cloudAuth
	orgID := cloudAuthOrgID(auth)
	return func() tea.Msg {
		orgs, err := listOrgsFromCloud(ctx, auth)
		if err != nil {
			return discoverTabsOrgMsg{}
		}
		for _, org := range orgs {
			if org.GetId() == orgID {
				return discoverTabsOrgMsg{name: org.GetName()}
			}
		}
		return discoverTabsOrgMsg{}
	}
}

func (m discoverTabsModel) updateLocal(msg tea.Msg) (discoverTabsModel, tea.Cmd) {
	updated, cmd := m.local.Update(msg)
	m.local = updated.(discoverModel)
	if m.local.quitting {
		m.cancelled = true
		return m, tea.Quit
	}
	return m, tagDiscoverTabsCmd(cmd, devicePickerLocalTab)
}

func (m discoverTabsModel) updateCloud(msg tea.Msg) (discoverTabsModel, tea.Cmd) {
	updated, cmd := m.cloud.Update(msg)
	m.cloud = updated.(cloudDiscoverModel)
	if m.cloud.quitting {
		m.cancelled = true
		return m, tea.Quit
	}
	return m, tagDiscoverTabsCmd(cmd, devicePickerCloudTab)
}

func (m discoverTabsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case discoverTabsLocalMsg:
		return m.updateLocal(msg.msg)
	case discoverTabsCloudMsg:
		if m.cloudAuth == nil {
			return m, nil
		}
		return m.updateCloud(msg.msg)
	case discoverTabsOrgMsg:
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

func (m discoverTabsModel) View() string {
	if m.cancelled || m.action != devicePickerNoAction {
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
		body.WriteString("Discover cloud devices\n")
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
