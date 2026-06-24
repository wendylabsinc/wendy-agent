package config

import (
	"errors"
	"fmt"
)

// ErrNotLoggedIn is returned when no auth sessions are stored.
var ErrNotLoggedIn = errors.New("not logged in; run 'wendy auth login' first")

// ErrMultipleSessions wraps the resolver error raised when several sessions
// exist, no --cloud-grpc flag was given, no valid default is set, and no
// interactive picker is available. Callers may match it with errors.Is to
// substitute a surface-specific message (e.g. the MCP tool's cloud_grpc wording).
var ErrMultipleSessions = errors.New("multiple auth sessions exist")

// SessionPicker selects one session interactively. It is injected by callers
// that can show a TUI; non-interactive callers (MCP, non-TTY) pass nil.
type SessionPicker func(cfg *Config) (*AuthConfig, error)

// DefaultAuth resolves DefaultCloudGRPC to a stored session. ok is false when
// no default is set or the named session no longer exists (stale default).
func (c *Config) DefaultAuth() (*AuthConfig, bool) {
	if c == nil || c.DefaultCloudGRPC == "" {
		return nil, false
	}
	for i := range c.Auth {
		if c.Auth[i].CloudGRPC == c.DefaultCloudGRPC {
			return &c.Auth[i], true
		}
	}
	return nil, false
}

// ResolveAuth chooses the auth session to use. Precedence:
//  1. cloudGRPC flag set      -> exact endpoint match (error if none)
//  2. exactly one session     -> use it
//  3. valid persisted default -> use it
//  4. pick != nil             -> interactive picker
//  5. otherwise               -> ErrMultipleSessions
//
// The returned session is guaranteed to hold certificate material.
func ResolveAuth(cfg *Config, cloudGRPC string, pick SessionPicker) (*AuthConfig, error) {
	if cfg == nil || len(cfg.Auth) == 0 {
		return nil, ErrNotLoggedIn
	}
	if cloudGRPC != "" {
		for i := range cfg.Auth {
			if cfg.Auth[i].CloudGRPC == cloudGRPC {
				return authWithCerts(&cfg.Auth[i])
			}
		}
		return nil, fmt.Errorf("no auth session for %s; run 'wendy auth login --cloud-grpc %s' first", cloudGRPC, cloudGRPC)
	}
	if len(cfg.Auth) == 1 {
		return authWithCerts(&cfg.Auth[0])
	}
	if def, ok := cfg.DefaultAuth(); ok {
		return authWithCerts(def)
	}
	if pick != nil {
		picked, err := pick(cfg)
		if err != nil {
			return nil, err
		}
		return authWithCerts(picked)
	}
	return nil, fmt.Errorf("%w; pass --cloud-grpc or run 'wendy auth use' to choose a default", ErrMultipleSessions)
}

// authWithCerts rejects sessions with no certificate material.
func authWithCerts(a *AuthConfig) (*AuthConfig, error) {
	if len(a.Certificates) == 0 {
		return nil, fmt.Errorf("auth session %s has no certificates; re-run 'wendy auth login'", a.CloudGRPC)
	}
	return a, nil
}
