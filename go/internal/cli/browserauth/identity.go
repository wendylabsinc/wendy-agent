package browserauth

import (
	"crypto/x509"

	"github.com/wendylabsinc/wendy-go/identity"
	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func (c Settings) identitySettings() identity.BrowserSettings {
	return identity.BrowserSettings{
		AuthBase:         c.AuthBase,
		ClientID:         c.ClientID,
		IdentityResource: c.IdentityResource,
		IdentityEndpoint: c.IdentityEndpoint,
		CloudResource:    c.CloudResource,
		CloudGRPC:        c.CloudGRPC,
		RelayIssuer:      c.RelayIssuer,
	}
}

// The host keeps its key, storage and mutex. This protocol snapshot contains
// only the shared state; copy it back even when a callback is refused so its
// single-use state and issuer observations retain the established behavior.
func (s *Session) identityFlow() (*identity.BrowserSession, error) {
	settings := s.settings().identitySettings()
	flow := &identity.BrowserSession{
		Client:                 identityhost.Client(s.Client),
		Settings:               &settings,
		RequireHostedMCPPolicy: s.RequireHostedMCPPolicy,
		Metadata:               s.meta,
		State:                  s.state,
		Verifier:               s.verifier,
		Redirect:               s.redirect,
		Started:                s.started,
		Tokens:                 s.tokens,
		Profile:                s.profile,
	}
	if s.key != nil {
		signer, err := identityhost.OperatorKey(s.key)
		if err != nil {
			return nil, err
		}
		flow.Key = signer
	}
	session := (config.AuthConfig{Certificates: []config.CertificateInfo{s.certificate}}).IdentitySession()
	flow.Certificate = session.Certificates[0]
	return flow, nil
}
func (s *Session) applyIdentityFlow(flow *identity.BrowserSession) {
	s.meta = flow.Metadata
	s.state = flow.State
	s.verifier = flow.Verifier
	s.redirect = flow.Redirect
	s.started = flow.Started
	s.tokens = flow.Tokens
	s.profile = flow.Profile
	s.certificate = config.CertificateInfo{}
	if flow.Certificate.PemCertificate != "" {
		s.certificate = identityhost.Certificate(flow.Certificate, s.privatePEM)
	}
}

func splitCertificateChainPEM(chain []byte) (string, string, *x509.Certificate, error) {
	return identityhost.SplitCertificateChainPEM(chain)
}
