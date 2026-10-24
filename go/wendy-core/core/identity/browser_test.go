package identity

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

func browserFixture(f *identityFixture) *BrowserSession {
	return &BrowserSession{Client: f.client, Settings: &BrowserSettings{AuthBase: "https://auth.example", ClientID: "cloud-login", IdentityResource: DefaultIdentityResource, IdentityEndpoint: "https://identity.example/v1/identity/certificate", CloudResource: "https://cloud.example/api", CloudGRPC: "cloud.example:443", RelayIssuer: "https://cloud.example"}}
}
func TestBrowserSessionSignInUsesHostKeyAndPreservesPolicy(t *testing.T) {
	f := fixture(t)
	s := browserFixture(f)
	target, err := s.Begin(context.Background(), "", fixtureIssuer, "https://app.example/auth/callback", func(context.Context) (platform.IdentityKey, error) {
		if len(f.calls) != 1 {
			t.Fatal("generated key before issuer discovery")
		}
		return f.key, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	if strings.Contains(target, "+") || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("browser authorization query changed")
	}
	if _, err := s.Complete(context.Background(), "code", s.State, fixtureIssuer); err != nil {
		t.Fatal(err)
	}
	if s.Tokens.Refresh != "rotated-2" || s.Profile.Tenant != fixtureTenant || s.Profile.Subject != "user-1" || s.State != "" {
		t.Fatal("browser session was not completed")
	}
}
func TestBrowserSessionRefusesExpiredOrReplayedCallback(t *testing.T) {
	f := fixture(t)
	s := browserFixture(f)
	s.State = "state"
	s.Started = time.Now().Add(-11 * time.Minute)
	if _, err := s.Complete(context.Background(), "code", "state", fixtureIssuer); err == nil || s.State != "" {
		t.Fatal("expired callback was not consumed")
	}
	s.State = "state"
	s.Started = time.Now()
	s.Metadata = BrowserMetadata{Issuer: fixtureIssuer}
	if _, err := s.Complete(context.Background(), "code", "state", "https://auth.example/realms/other"); err == nil || s.State != "" {
		t.Fatal("issuer mismatch did not consume state")
	}
	if len(f.calls) != 0 {
		t.Fatal("refused callback sent network traffic")
	}
}
func TestBrowserSessionEnforcesHostedPolicyBeforeIssuingCertificate(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		f := fixture(t)
		s := browserFixture(f)
		s.RequireHostedMCPPolicy = true
		f.alter = func(claims map[string]any) { claims["allow_mcp"] = true; claims["allow_hosted_mcp"] = allowed }
		_, err := s.Begin(context.Background(), "", fixtureIssuer, "https://app.example/auth/callback", func(context.Context) (platform.IdentityKey, error) { return f.key, nil })
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Complete(context.Background(), "code", s.State, fixtureIssuer)
		if allowed {
			if err != nil {
				t.Fatal(err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "does not allow hosted MCP") || f.key.requests != 0 {
			t.Fatalf("hosted refusal: %v", err)
		}
	}
}
