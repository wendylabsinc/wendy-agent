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

// authSessionKey returns a unique string identifying an auth session: the gRPC
// endpoint combined with the cert org ID. Sessions for different orgs on the
// same endpoint are distinct and must not be collapsed to a single picker row.
func authSessionKey(a *config.AuthConfig) string {
	if len(a.Certificates) > 0 {
		return fmt.Sprintf("%s::%d", a.CloudGRPC, a.Certificates[0].OrganizationID)
	}
	return a.CloudGRPC
}

// authPickerItems builds picker rows for every stored session. The Name column
// shows "Org <ID>" (or the endpoint when no cert is present), and the
// Description shows the dashboard URL so the user can identify the environment.
// DedupKey and Value carry the session key so each (endpoint, org) pair is a
// distinct row even when multiple orgs share the same gRPC endpoint.
func authPickerItems(cfg *config.Config) []tui.PickerItem {
	items := make([]tui.PickerItem, 0, len(cfg.Auth))
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		key := authSessionKey(a)
		name := authSessionLabel(a)
		desc := a.CloudDashboard
		if desc == "" {
			desc = a.CloudGRPC
		}
		items = append(items, tui.PickerItem{
			Name:        name,
			Description: desc,
			DedupKey:    key,
			Value:       key,
		})
	}
	return items
}

// pickAuthSession shows the interactive session picker. 'd' marks the
// highlighted session as the persisted default (written immediately, mirroring
// the device picker), 'x' clears it, and Enter selects a session for this
// invocation only. Returns the selected session (cert-validated).
func pickAuthSession(cfg *config.Config) (*config.AuthConfig, error) {
	picker := tui.NewPickerWithTitle("Select an organisation")
	// Compute the default key from the stored default org ID (preferred) or
	// the legacy DefaultCloudGRPC field so both code-paths work.
	if cfg.DefaultOrgID != 0 {
		for i := range cfg.Auth {
			if key := authSessionKey(&cfg.Auth[i]); strings.HasSuffix(key, fmt.Sprintf("::%d", cfg.DefaultOrgID)) {
				picker.DefaultKey = strings.ToLower(key)
				break
			}
		}
	}
	if picker.DefaultKey == "" && cfg.DefaultCloudGRPC != "" {
		for i := range cfg.Auth {
			if cfg.Auth[i].CloudGRPC == cfg.DefaultCloudGRPC {
				picker.DefaultKey = strings.ToLower(authSessionKey(&cfg.Auth[i]))
				break
			}
		}
	}

	picker.OnSetDefault = func(item tui.PickerItem) string {
		key, _ := item.Value.(string)
		if key == "" {
			return ""
		}
		c, err := config.Load()
		if err != nil {
			return fmt.Sprintf("Could not save default: %v", err)
		}
		// Persist as DefaultCloudGRPC (the endpoint portion before "::").
		endpoint := key
		if idx := strings.Index(key, "::"); idx >= 0 {
			endpoint = key[:idx]
		}
		c.DefaultCloudGRPC = endpoint
		_ = config.Save(c)
		return fmt.Sprintf("Default set to %s.", item.Name)
	}
	picker.OnUnsetDefault = func() string {
		if c, err := config.Load(); err == nil {
			c.DefaultCloudGRPC = ""
			_ = config.Save(c)
		}
		return "Default cleared."
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
		return nil, fmt.Errorf("no organisation selected")
	}
	selectedKey := pm.Selected().Value.(string)
	for i := range cfg.Auth {
		if authSessionKey(&cfg.Auth[i]) == selectedKey {
			if len(cfg.Auth[i].Certificates) == 0 {
				return nil, fmt.Errorf("auth session has no certificates; re-run 'wendy auth login'")
			}
			return &cfg.Auth[i], nil
		}
	}
	return nil, fmt.Errorf("selected session no longer exists")
}

// pickAuthSessionFn is the indirection point so tests can stub the picker.
var pickAuthSessionFn = pickAuthSession
