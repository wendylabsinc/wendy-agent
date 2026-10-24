package identity

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
type SessionPicker func(cfg *SessionSet) (*Session, error)

// DefaultAuth resolves DefaultCloudGRPC to a stored session. ok is false when
// no default is set or the named session no longer exists (stale default).
func (c *SessionSet) DefaultAuth() (*Session, bool) {
	if c == nil || c.DefaultCloudGRPC == "" {
		return nil, false
	}
	if c.DefaultTenantUUID != "" {
		for i := range c.Auth {
			a := &c.Auth[i]
			if a.CloudGRPC == c.DefaultCloudGRPC && len(a.Certificates) > 0 && a.Certificates[0].TenantUUID() == c.DefaultTenantUUID {
				return a, true
			}
		}
	}
	var preferred *Session
	selectedOrg := ""
	for i := range c.Auth {
		if c.Auth[i].CloudGRPC == c.DefaultCloudGRPC {
			if selectedOrg == "0" && len(c.Auth[i].Certificates) > 0 && c.Auth[i].Certificates[0].TenantUUID() != "" {
				preferred = nil
			}
			if preferred == nil {
				selectedOrg = c.Auth[i].OrganizationKey()
			}
			if c.Auth[i].OrganizationKey() != selectedOrg {
				continue
			}
			preferred = preferAuth(preferred, &c.Auth[i])
		}
	}
	return preferred, preferred != nil
}

// preferAuth resolves legacy/operator duplicates for the same selection. The
// Cloud request-signing contract makes an operator session strictly more
// capable: it has a refreshable bearer token and can sign privileged writes,
// while the legacy session cannot satisfy the operator-signature gate.
func preferAuth(current, candidate *Session) *Session {
	if current == nil || (current.OAuthIssuer == "" && candidate.OAuthIssuer != "") {
		return candidate
	}
	return current
}

// ResolveSession chooses the auth session to use. Precedence:
//  1. cloudGRPC flag set  -> endpoint match; prefer the current context on that
//     endpoint, else the operator-preferred session for its first org
//  2. exactly one session -> use it
//  3. CurrentContext set  -> the session with that name
//  4. pick != nil         -> interactive picker
//  5. otherwise           -> ErrMultipleSessions
//
// The returned session is guaranteed to hold certificate material or an API token.
func ResolveSession(cfg *SessionSet, cloudGRPC string, pick SessionPicker) (*Session, error) {
	if cfg == nil || len(cfg.Auth) == 0 {
		return nil, ErrNotLoggedIn
	}
	// Names + one-time migration of the legacy default fields into CurrentContext.
	// Idempotent; a no-op for a config already loaded via Load. This lets callers
	// that build a SessionSet literal (tests, in-process fixtures) resolve by context
	// without a Load round-trip.
	ensureContexts(cfg)

	if cloudGRPC != "" {
		var matches []*Session
		for i := range cfg.Auth {
			if cfg.Auth[i].CloudGRPC == cloudGRPC {
				matches = append(matches, &cfg.Auth[i])
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no auth session for %s; run 'wendy auth login --cloud-grpc %s' first", cloudGRPC, cloudGRPC)
		}
		// The current context wins when it lives on this endpoint; otherwise the
		// flag alone cannot name an org, so fall back to the operator-preferred
		// session for the endpoint's first org (not the oldest login).
		if cfg.CurrentContext != "" {
			for _, m := range matches {
				if m.Name == cfg.CurrentContext {
					return authWithCerts(m)
				}
			}
		}
		var preferred *Session
		selectedOrg := matches[0].OrganizationKey()
		if selectedOrg == "0" {
			for _, m := range matches {
				if len(m.Certificates) > 0 && m.Certificates[0].TenantUUID() != "" {
					selectedOrg = m.OrganizationKey()
					break
				}
			}
		}
		for _, match := range matches {
			if match.OrganizationKey() != selectedOrg {
				continue
			}
			preferred = preferAuth(preferred, match)
		}
		return authWithCerts(preferred)
	}
	if len(cfg.Auth) == 1 {
		return authWithCerts(&cfg.Auth[0])
	}
	if cfg.CurrentContext != "" {
		if a, ok := cfg.ContextByName(cfg.CurrentContext); ok {
			return authWithCerts(a)
		}
		// Stale CurrentContext (its session was removed); fall through to the
		// picker or error rather than silently using another context.
	}
	if pick != nil {
		picked, err := pick(cfg)
		if err != nil {
			return nil, err
		}
		return authWithCerts(picked)
	}
	return nil, fmt.Errorf("%w; pass --cloud-grpc or run 'wendy auth use' to choose a context", ErrMultipleSessions)
}

// authWithCerts rejects sessions with no certificate material.
func authWithCerts(a *Session) (*Session, error) {
	if len(a.Certificates) == 0 && !a.HasAPIKey() {
		return nil, fmt.Errorf("auth session %s has no certificates or API token; re-run 'wendy auth login'", a.CloudGRPC)
	}
	return a, nil
}
