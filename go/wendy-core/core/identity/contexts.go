package identity

import (
	"fmt"
	"strings"
)

// EnsureContexts assigns names to any unnamed auth entries and, on a config
// with no context chosen yet, seeds CurrentContext. Login writers call it after
// AddAuth so the new session becomes a named context (the first login becomes
// "default" and current). Idempotent.
func (c *SessionSet) EnsureContexts() { ensureContexts(c) }

// ContextByName returns the auth context (session) with the given name.
func (c *SessionSet) ContextByName(name string) (*Session, bool) {
	if c == nil || name == "" {
		return nil, false
	}
	for i := range c.Auth {
		if c.Auth[i].Name == name {
			return &c.Auth[i], true
		}
	}
	return nil, false
}

// ensureContexts assigns a stable name to every auth entry that lacks one and,
// on first migration, seeds CurrentContext from the legacy default-selection
// fields (DefaultTenantUUID/DefaultOrgID/DefaultCloudGRPC). It is idempotent
// and mutates cfg in place without touching disk; the next Save persists the
// names. Naming is deterministic (Auth order is stable), so `auth use <name>`
// is usable even before that first Save.
func ensureContexts(c *SessionSet) {
	if c == nil || len(c.Auth) == 0 {
		return
	}

	// Seed only on the first migration: nothing named yet and no context chosen.
	// Resolve the legacy default before naming, then record which entry it was so
	// the operator-preferred default stays current after migration.
	var legacyDefault *Session
	if c.CurrentContext == "" {
		legacyDefault = c.legacyDefaultAuth()
	}

	used := make(map[string]bool, len(c.Auth))
	for i := range c.Auth {
		if c.Auth[i].Name != "" {
			used[c.Auth[i].Name] = true
		}
	}
	for i := range c.Auth {
		if c.Auth[i].Name != "" {
			continue
		}
		var name string
		if i == 0 && !used["default"] {
			// The first login is always "default" (ruling 8c1b6fec item 11).
			name = "default"
		} else {
			name = uniqueContextName(contextNameFor(&c.Auth[i]), used)
		}
		c.Auth[i].Name = name
		used[name] = true
	}

	if c.CurrentContext == "" {
		switch {
		case legacyDefault != nil:
			c.CurrentContext = legacyDefault.Name
		case len(c.Auth) == 1:
			c.CurrentContext = c.Auth[0].Name
		}
	}
}

// legacyDefaultAuth resolves the pre-context default-selection fields to a
// session, mirroring the no-flag precedence the resolver used before contexts
// (DefaultTenantUUID, then DefaultOrgID, then DefaultCloudGRPC). Used once at
// migration to seed CurrentContext; it never calls ensureContexts.
func (c *SessionSet) legacyDefaultAuth() *Session {
	if c.DefaultTenantUUID != "" {
		if auth, ok := c.DefaultAuth(); ok &&
			len(auth.Certificates) > 0 && auth.Certificates[0].TenantUUID() == c.DefaultTenantUUID {
			return auth
		}
	}
	if c.DefaultOrgID != 0 {
		var preferred *Session
		for i := range c.Auth {
			a := &c.Auth[i]
			if len(a.Certificates) > 0 && int32(a.Certificates[0].OrganizationID) == c.DefaultOrgID {
				preferred = preferAuth(preferred, a)
			}
		}
		if preferred != nil {
			return preferred
		}
	}
	if def, ok := c.DefaultAuth(); ok {
		return def
	}
	return nil
}

// contextNameFor derives a human name for a session that has none: the realm
// from an OIDC issuer, else the legacy numeric org id, else the endpoint host.
// UUID-org (OIDC) sessions carry no numeric org id, so the realm comes first.
func contextNameFor(a *Session) string {
	if a.OAuthIssuer != "" {
		if realm := issuerRealmName(a.OAuthIssuer); realm != "" {
			return realm
		}
	}
	if org := authEntryOrgID(*a); org > 0 {
		return fmt.Sprintf("org-%d", org)
	}
	if host := hostOnly(a.CloudGRPC); host != "" {
		return host
	}
	return "context"
}

// uniqueContextName returns base, or base-2/base-3/... if base is already taken.
func uniqueContextName(base string, used map[string]bool) string {
	if base == "" {
		base = "context"
	}
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		if !used[cand] {
			return cand
		}
	}
}

// issuerRealmName returns the last path segment of a realm issuer URL, e.g.
// "https://auth.wendy.sh/realms/acme" -> "acme".
func issuerRealmName(issuer string) string {
	issuer = strings.TrimSuffix(issuer, "/")
	if i := strings.LastIndex(issuer, "/"); i >= 0 && i+1 < len(issuer) {
		return issuer[i+1:]
	}
	return ""
}

// hostOnly strips the :port from a host:port gRPC endpoint.
func hostOnly(endpoint string) string {
	if i := strings.LastIndex(endpoint, ":"); i > 0 {
		return endpoint[:i]
	}
	return endpoint
}
