package config

import "github.com/wendylabsinc/wendy-go/identity"

// IdentitySession supplies portable session metadata without loading host secrets.
// Certificate parsing and secret-store references remain host responsibilities.
func (a AuthConfig) IdentitySession() identity.Session {
	session := identity.Session{
		Name:            a.Name,
		CloudDashboard:  a.CloudDashboard,
		CloudGRPC:       a.CloudGRPC,
		OAuthIssuer:     a.OAuthIssuer,
		OAuthClientID:   a.OAuthClientID,
		OAuthResource:   a.OAuthResource,
		PKIResource:     a.PKIResource,
		PKIEndpoint:     a.PKIEndpoint,
		APIKey:          a.APIKey,
		RefreshToken:    a.RefreshToken,
		OAuthExpiresAt:  a.OAuthExpiresAt,
		ServiceAccount:  a.ServiceAccount,
		LiteCertificate: a.LiteCertificate,
	}
	for _, certificate := range a.Certificates {
		principal := certificate.PrincipalURI
		if principal == "" {
			principal = certificate.CertificatePrincipal()
		}
		if certificate.TenantUUID() == "" {
			principal = ""
		}
		session.Certificates = append(session.Certificates, identity.Certificate{PemCertificate: certificate.PemCertificate, PemCertificateChain: certificate.PemCertificateChain, PrincipalURI: principal, OrganizationID: certificate.OrganizationID})
	}
	return session
}

func (c *Config) identityState() *identity.SessionSet {
	if c == nil {
		return nil
	}
	state := &identity.SessionSet{CurrentContext: c.CurrentContext, DefaultCloudGRPC: c.DefaultCloudGRPC, DefaultTenantUUID: c.DefaultTenantUUID, DefaultOrgID: c.DefaultOrgID}
	for _, auth := range c.Auth {
		state.Auth = append(state.Auth, auth.IdentitySession())
	}
	return state
}
func (c *Config) applyContextState(state *identity.SessionSet) {
	if c == nil || state == nil {
		return
	}
	c.CurrentContext = state.CurrentContext
	for i := range c.Auth {
		c.Auth[i].Name = state.Auth[i].Name
	}
}
func (c *Config) hostSession(state *identity.SessionSet, session *identity.Session) *AuthConfig {
	if session == nil {
		return nil
	}
	for i := range state.Auth {
		if &state.Auth[i] == session {
			return &c.Auth[i]
		}
	}
	return nil
}
