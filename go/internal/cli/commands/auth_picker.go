//go:build darwin || linux || windows

package commands

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// authSessionLabel renders a session for humans: "org <N> — <endpoint>", or
// just the endpoint when the session holds no certificate (and thus no org).
func authSessionLabel(a *config.AuthConfig) string {
	if len(a.Certificates) > 0 {
		return fmt.Sprintf("org %d — %s", a.Certificates[0].OrganizationID, a.CloudGRPC)
	}
	return a.CloudGRPC
}

// authPickerItems builds picker rows for every stored session. The Name column
// shows the dashboard URL (falling back to the gRPC endpoint), the Description
// shows "org N — endpoint", and both Value and DedupKey carry the endpoint so
// selection and default-marking key on the session identity.
func authPickerItems(cfg *config.Config) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(cfg.Auth))
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		name := a.CloudDashboard
		if name == "" {
			name = a.CloudGRPC
		}
		items = append(items, tui.PickerItem{
			Name:        name,
			Description: authSessionLabel(a),
			DedupKey:    a.CloudGRPC,
			Value:       a.CloudGRPC,
		})
	}
	return items
}

// pickAuthSession shows the interactive session picker. 'd' marks the
// highlighted session as the persisted default (written immediately, mirroring
// the device picker), 'x' clears it, and Enter selects a session for this
// invocation only. Returns the selected session (cert-validated).
func pickAuthSession(cfg *config.Config) (*config.AuthConfig, error) {
	picker := tui.NewPickerWithTitle("Select the Wendy Cloud session to use")
	picker.DefaultKey = strings.ToLower(cfg.DefaultCloudGRPC)
	picker.OnSetDefault = func(item tui.PickerItem) {
		endpoint, _ := item.Value.(string)
		if endpoint == "" {
			return
		}
		if c, err := config.Load(); err == nil {
			c.DefaultCloudGRPC = endpoint
			_ = config.Save(c)
		}
	}
	picker.OnUnsetDefault = func() {
		if c, err := config.Load(); err == nil {
			c.DefaultCloudGRPC = ""
			_ = config.Save(c)
		}
	}

	p := tea.NewProgram(picker)
	go func() {
		p.Send(tui.PickerAddMsg{Items: authPickerItems(cfg)})
		p.Send(tui.PickerDoneMsg{})
	}()

	finalModel, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("picker: %w", err)
	}
	pm := finalModel.(tui.PickerModel)
	if pm.Cancelled() {
		return nil, ErrUserCancelled
	}
	if pm.Selected() == nil {
		return nil, fmt.Errorf("no session selected")
	}
	endpoint := pm.Selected().Value.(string)
	for i := range cfg.Auth {
		if cfg.Auth[i].CloudGRPC == endpoint {
			if len(cfg.Auth[i].Certificates) == 0 {
				return nil, fmt.Errorf("auth session %s has no certificates; re-run 'wendy auth login'", endpoint)
			}
			return &cfg.Auth[i], nil
		}
	}
	return nil, fmt.Errorf("selected session %s no longer exists", endpoint)
}

// pickAuthSessionFn is the indirection point so tests can stub the picker.
var pickAuthSessionFn = pickAuthSession
