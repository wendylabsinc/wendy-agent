// Package browserauth implements the CLI's PKCE, DPoP and operator certificate flow
// without filesystem storage or a native loopback listener.
package browserauth

import (
	"context"
	"crypto"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"

	"github.com/wendylabsinc/wendy-go/identity"

	"github.com/wendylabsinc/wendy/go/internal/cli/clouddefaults"
	"github.com/wendylabsinc/wendy/go/internal/cli/cloudrequest"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const AuthBase = "https://auth.dev.wendy.sh"
const IdentityResource = "https://pki.wendy.sh/identity"
const IdentityEndpoint = "https://identity.dev.pki.wendy.sh/v1/identity/certificate"
const CloudResource = "https://cloud.dev.wendy.sh/api"
const ClientID = "cloud-login"

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type metadata = identity.BrowserMetadata
type token = identity.BrowserToken
type Profile = identity.BrowserProfile
type Session struct {
	Settings *Settings
	Store    CredentialStore
	// RequireHostedMCPPolicy is set by the hosted gateway, never from saved
	// credentials. Its verified user tokens must permit both MCP channels.
	RequireHostedMCPPolicy                bool
	mu                                    sync.Mutex
	Client                                HTTPDoer
	meta                                  metadata
	key                                   crypto.Signer
	privatePEM, state, verifier, redirect string
	started                               time.Time
	certificate                           config.CertificateInfo
	tokens                                token
	profile                               Profile
}

func validIssuer(raw string) bool {
	return (&Session{}).validIssuer(raw)
}

func (s *Session) discover(ctx context.Context, issuer string) (metadata, error) {
	flow, err := s.identityFlow()
	if err != nil {
		return metadata{}, err
	}
	return flow.Discover(ctx, issuer)
}
func (s *Session) Begin(ctx context.Context, email, issuer, redirect string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, err := s.identityFlow()
	if err != nil {
		return "", err
	}
	target, err := flow.Begin(ctx, email, issuer, redirect, func(context.Context) (wendy.IdentityKey, error) {
		privatePEM, err := certs.GenerateMLDSAKeyPair()
		if err != nil {
			return nil, err
		}
		key, err := certs.ParseSigningPrivateKeyPEM([]byte(privatePEM))
		if err != nil {
			return nil, err
		}
		signer, err := identityhost.OperatorKey(key)
		if err != nil {
			return nil, err
		}
		s.key = key
		s.privatePEM = privatePEM
		return signer, nil
	})
	s.applyIdentityFlow(flow)
	return target, err
}
func (s *Session) exchange(ctx context.Context, form url.Values) (token, error) {
	flow, err := s.identityFlow()
	if err != nil {
		return token{}, err
	}
	return flow.Exchange(ctx, form)
}
func (s *Session) Complete(ctx context.Context, code, state, issuer string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	flow, err := s.identityFlow()
	if err != nil {
		return Profile{}, err
	}
	_, err = flow.Complete(ctx, code, state, issuer)
	s.applyIdentityFlow(flow)
	if err != nil {
		return Profile{}, err
	}
	if err := s.saveLocked(); err != nil {
		s.profile.ProfileWarning = "Could not remember this sign-in: " + err.Error()
	}
	return s.profile, nil
}

// Verify both token signature and sender binding before displaying a signed-in identity.
func (s *Session) verifyAccess(ctx context.Context, raw, audience string) (map[string]any, error) {
	flow, err := s.identityFlow()
	if err != nil {
		return nil, err
	}
	return flow.VerifyAccess(ctx, raw, audience)
}

func canonicalUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Discover uses the CLI's v2 API and its per-RPC DPoP proof. The supplied dialer
// must carry HTTP/2 to the fixed Cloud authority over an authenticated TLS relay.
func (s *Session) Discover(ctx context.Context, dial func(context.Context, string) (net.Conn, error)) ([]*cloudpbv2.Asset, error) {
	return s.DiscoverFiltered(ctx, dial, true)
}

func (s *Session) DiscoverFiltered(ctx context.Context, dial func(context.Context, string) (net.Conn, error), onlineOnly bool) ([]*cloudpbv2.Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens.Access == "" {
		return nil, errors.New("Sign in with Wendy first")
	}
	if err := s.refreshCloudToken(ctx); err != nil {
		return nil, err
	}
	auth := &config.AuthConfig{CloudGRPC: s.settings().CloudGRPC, OAuthIssuer: s.meta.Issuer, DPoPPrivateKey: s.privatePEM, Certificates: []config.CertificateInfo{s.certificate}}
	options := []grpc.DialOption{clouddefaults.TunnelDialer(func(ctx context.Context) (net.Conn, error) { return dial(ctx, s.settings().CloudGRPC) }), grpc.WithTransportCredentials(insecure.NewCredentials())}
	options = append(options, cloudrequest.DPoPDialOptions(auth, func(context.Context) (string, crypto.Signer, error) { return s.tokens.Access, s.key, nil })...)
	conn, e := grpc.NewClient("passthrough:///"+s.settings().CloudGRPC, options...)
	if e != nil {
		return nil, e
	}
	defer conn.Close()
	if s.profile.Email == "" {
		if err := s.loadUserInfo(ctx); err != nil {
			s.profile.ProfileWarning = "Email lookup unavailable: " + err.Error()
		}
	}
	if s.profile.Organization == "" {
		orgCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		org, err := cloudpbv2.NewOrganizationServiceClient(conn).GetOrganization(orgCtx, &cloudpbv2.GetOrganizationRequest{Id: s.profile.Tenant})
		cancel()
		if err != nil {
			s.profile.ProfileWarning = "Organization lookup unavailable: " + err.Error()
		} else if org.GetId() != s.profile.Tenant {
			s.profile.ProfileWarning = "Organization lookup returned a different tenant"
		} else {
			s.profile.Organization = org.GetName()
			if s.profile.Email != "" {
				s.profile.ProfileWarning = ""
			}
		}
	}
	if err := s.saveLocked(); err != nil {
		s.profile.ProfileWarning = "Could not save this sign-in: " + err.Error()
	}
	client := cloudpbv2.NewAssetServiceClient(conn)
	assets := make([]*cloudpbv2.Asset, 0)
	compute := true
	limit := int32(200)
	for offset := int32(0); ; {
		stream, e := client.ListAssets(ctx, &cloudpbv2.ListAssetsRequest{OrganizationId: s.profile.Tenant, IsComputeDevice: &compute, OnlineOnly: &onlineOnly, Offset: &offset, Limit: &limit})
		if e != nil {
			return nil, e
		}
		count, total := int32(0), int32(0)
		for {
			item, e := stream.Recv()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			if item.Asset == nil {
				return nil, errors.New("Cloud returned an empty device record")
			}
			if item.Asset.GetOrganizationId() != s.profile.Tenant || !canonicalUUID(item.Asset.GetId()) {
				return nil, errors.New("Cloud returned a device outside the signed-in tenant or an invalid asset ID")
			}
			if len(assets) >= 10000 {
				return nil, errors.New("Cloud device limit exceeded")
			}
			assets = append(assets, item.Asset)
			count++
			total = item.Total
		}
		offset += count
		if count == 0 || offset >= total {
			return assets, nil
		}
	}
}

// Realm discovery returns a root-relative loginURL. Resolve it only against
// the configured auth host, while also accepting its absolute equivalent.
func issuerFromRealmLoginURL(raw string) (string, error) {
	return (&Session{}).issuerFromRealmLoginURL(raw)
}

func (s *Session) issuerFromRealmLoginURL(raw string) (string, error) {
	flow, err := s.identityFlow()
	if err != nil {
		return "", err
	}
	return flow.IssuerFromRealmLoginURL(raw)
}

// UserInfo carries scope-gated email claims that access tokens may omit.
// Bind both the request and response to the authenticated operator.
func (s *Session) loadUserInfo(ctx context.Context) error {
	flow, err := s.identityFlow()
	if err != nil {
		return err
	}
	err = flow.LoadUserInfo(ctx)
	s.applyIdentityFlow(flow)
	return err
}
func (s *Session) Profile() Profile { s.mu.Lock(); defer s.mu.Unlock(); return s.profile }
