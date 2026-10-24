package browserauth

import "github.com/wendylabsinc/wendy-go/identity"

// Settings pins the identity, PKI and Cloud services for a deployment. Tool
// arguments and OAuth redirect parameters cannot change these destinations.
type Settings struct {
	AuthBase         string `json:"auth_base"`
	ClientID         string `json:"client_id"`
	IdentityResource string `json:"identity_resource"`
	IdentityEndpoint string `json:"identity_endpoint"`
	CloudResource    string `json:"cloud_resource"`
	CloudGRPC        string `json:"cloud_grpc"`
	RelayIssuer      string `json:"relay_issuer"`
}

func (s *Session) settings() Settings {
	if s.Settings != nil {
		return *s.Settings
	}
	return Settings{AuthBase, ClientID, IdentityResource, IdentityEndpoint, CloudResource, "api.dev.wendy.sh:443", "https://api.dev.wendy.sh"}
}

func (c Settings) Validate() error { return c.identitySettings().Validate() }

func (s *Session) validIssuer(raw string) bool {
	settings := s.settings().identitySettings()
	return (&identity.BrowserSession{Settings: &settings}).ValidIssuer(raw)
}

// orgIssuer is a valid issuer other than the system realm, which is never a
// login target. Realm names are compared without case, so /realms/SYSTEM is
// still the system realm.
func (s *Session) orgIssuer(raw string) bool {
	settings := s.settings().identitySettings()
	return (&identity.BrowserSession{Settings: &settings}).OrgIssuer(raw)
}
