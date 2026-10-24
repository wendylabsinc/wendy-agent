package identity

import (
	"fmt"
)

// SessionSet owns portable context naming and selection. Hosts load and persist
// it as part of their configuration without moving filesystem access into Core.
type SessionSet struct {
	Auth                                                []Session
	CurrentContext, DefaultCloudGRPC, DefaultTenantUUID string
	DefaultOrgID                                        int32
}

// HasAPIKey reports whether credential metadata names an API token.
func (s Session) HasAPIKey() bool { return s.APIKey != "" }

func authEntryOrgID(a Session) int {
	if len(a.Certificates) > 0 {
		return a.Certificates[0].OrganizationID
	}
	return 0
}

// AddAuth adds or replaces an auth entry. Matching is by (cloudDashboard,
// cloudGRPC, orgID) so that multiple orgs on the same cloud endpoint each
// keep their own entry instead of overwriting one another.

func (c *SessionSet) AddAuth(auth Session) {
	incomingOrg := auth.OrganizationKey()
	for i, existing := range c.Auth {
		if existing.CloudDashboard == auth.CloudDashboard &&
			existing.CloudGRPC == auth.CloudGRPC &&
			existing.OAuthIssuer == auth.OAuthIssuer &&
			existing.ServiceAccount == auth.ServiceAccount &&
			existing.OrganizationKey() == incomingOrg {
			c.Auth[i] = auth
			return
		}
	}
	c.Auth = append(c.Auth, auth)
}

// OrganizationKey prefers the tenant UUID and retains the legacy numeric fallback.

func (a Session) OrganizationKey() string {
	if len(a.Certificates) == 0 {
		return "0"
	}
	if tenant := a.Certificates[0].TenantUUID(); tenant != "" {
		return tenant
	}
	return fmt.Sprint(a.Certificates[0].OrganizationID)
}
