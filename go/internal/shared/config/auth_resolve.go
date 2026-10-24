package config

import "github.com/wendylabsinc/wendy-go/identity"

var ErrNotLoggedIn = identity.ErrNotLoggedIn
var ErrMultipleSessions = identity.ErrMultipleSessions

// SessionPicker is the CLI's host-owned interactive session picker.
type SessionPicker func(*Config) (*AuthConfig, error)

func (c *Config) DefaultAuth() (*AuthConfig, bool) {
	state := c.identityState()
	selected, ok := state.DefaultAuth()
	return c.hostSession(state, selected), ok
}

// ResolveAuth preserves host object identities and picker mutations while the
// shared SDK owns endpoint/context selection, naming and credential checks.
func ResolveAuth(cfg *Config, endpoint string, pick SessionPicker) (*AuthConfig, error) {
	state := cfg.identityState()
	var sdkPicker identity.SessionPicker
	var picked *AuthConfig
	pickerCalled := false
	if pick != nil {
		sdkPicker = func(state *identity.SessionSet) (*identity.Session, error) {
			pickerCalled = true
			cfg.applyContextState(state)
			var err error
			picked, err = pick(cfg)
			if err != nil {
				return nil, err
			}
			session := picked.IdentitySession()
			return &session, nil
		}
	}
	selected, err := identity.ResolveSession(state, endpoint, sdkPicker)
	if !pickerCalled {
		cfg.applyContextState(state)
	}
	if err != nil {
		return nil, err
	}
	if picked != nil {
		return picked, nil
	}
	return cfg.hostSession(state, selected), nil
}
