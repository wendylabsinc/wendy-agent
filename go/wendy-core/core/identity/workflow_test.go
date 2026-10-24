package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

const fixtureTenant = "2558fd76-afc7-466e-9613-6b715296a526"
const fixtureIssuer = "https://auth.example/realms/acme"

type testHTTP func(*http.Request) (*http.Response, error)

func (f testHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
func jsonResponse(status int, body any) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(b)))}
}

type testIdentityKey struct {
	testSigner
	tenant, subject string
	fail            error
	requests        int
}

func (s *testIdentityKey) CertificateRequest(ctx context.Context, subject string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.requests++
	return []byte("host CSR for " + subject), nil
}
func (s *testIdentityKey) CheckCertificate(ctx context.Context, chain []byte) (platform.CertificateIdentity, error) {
	if ctx.Err() != nil {
		return platform.CertificateIdentity{}, ctx.Err()
	}
	if s.fail != nil {
		return platform.CertificateIdentity{}, s.fail
	}
	return platform.CertificateIdentity{CertificatePEM: string(chain), PrincipalURI: "spiffe://wendy.sh/tenant/" + s.tenant + "/operator/" + s.subject}, nil
}

type identityFixture struct {
	client   *Client
	key      *testIdentityKey
	realmKey *ecdsa.PrivateKey
	calls    []string
	tokens   int
	fail     string
	nonce    string
	alter    func(map[string]any)
}

func fixture(t *testing.T) *identityFixture {
	t.Helper()
	realmKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &identityFixture{realmKey: realmKey, key: &testIdentityKey{testSigner: testSigner{testKey(t)}, tenant: fixtureTenant, subject: "user-1"}}
	f.client = &Client{Verifier: testSignatureVerifier{}, HTTP: testHTTP(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		f.calls = append(f.calls, r.URL.Path)
		switch r.URL.Path {
		case "/realms/acme/.well-known/openid-configuration":
			return jsonResponse(200, ProviderMetadata{Issuer: fixtureIssuer, AuthorizationEndpoint: fixtureIssuer + "/authorize", TokenEndpoint: fixtureIssuer + "/oauth2/token", JWKSURI: fixtureIssuer + "/.well-known/jwks.json"}), nil
		case "/realms/acme/.well-known/jwks.json":
			if f.fail == "jwks" {
				return jsonResponse(503, map[string]string{}), nil
			}
			return jsonResponse(200, map[string]any{"keys": []JWK{testES256JWK(realmKey, "realm-key")}}), nil
		case "/realms/acme/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			f.tokens++
			thumbprint, err := OperatorJWKThumbprint(r.Context(), f.key)
			if err != nil {
				t.Fatal(err)
			}
			claims := map[string]any{"iss": fixtureIssuer, "aud": r.Form.Get("resource"), "sub": "user-1", "exp": time.Now().Add(time.Hour).Unix(), "cnf": map[string]string{"jkt": thumbprint}, "tenant_uuid": fixtureTenant}
			if f.fail == "cloud-subject" && r.Form.Get("resource") == "https://cloud.example/api" {
				claims["sub"] = "other"
			}
			if f.alter != nil {
				f.alter(claims)
			}
			access := testES256Token(t, realmKey, "realm-key", claims)
			claims["aud"] = "cloud-login"
			claims["nonce"] = f.nonce
			id := testES256Token(t, realmKey, "realm-key", claims)
			return jsonResponse(200, TokenResponse{AccessToken: access, RefreshToken: fmt.Sprintf("rotated-%d", f.tokens), IDToken: id, ExpiresIn: 3600, TokenType: "DPoP"}), nil
		case "/v1/identity/certificate":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != "host CSR for user-1" || !strings.HasPrefix(r.Header.Get("Authorization"), "DPoP ") || r.Header.Get("DPoP") == "" {
				t.Errorf("CSR request lost its host key or token binding")
			}
			if f.fail == "issue" {
				return jsonResponse(403, "refused"), nil
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("host-checked certificate"))}, nil
		}
		return nil, fmt.Errorf("unexpected request %s", r.URL)
	})}
	return f
}
func fixtureSession() *Session {
	return &Session{OAuthIssuer: fixtureIssuer, OAuthClientID: "cloud-login", OAuthResource: "https://cloud.example/api", PKIResource: DefaultIdentityResource, PKIEndpoint: "https://identity.example/v1/identity/certificate", RefreshToken: "old-refresh", Certificates: []Certificate{{PrincipalURI: "spiffe://wendy.sh/tenant/" + fixtureTenant + "/operator/user-1"}}}
}

func TestPortableLoginCompletesWithHostCapabilities(t *testing.T) {
	f := fixture(t)
	login, err := f.client.PrepareLogin(context.Background(), LoginOptions{Issuer: fixtureIssuer, ClientID: "cloud-login", CloudResource: "https://cloud.example/api", IdentityResource: DefaultIdentityResource, IdentityEndpoint: "https://identity.example/v1/identity/certificate", CloudGRPC: "cloud.example:443"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := login.BeginAuthorization("http://127.0.0.1:8765/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(target)
	f.nonce = u.Query().Get("nonce")
	callback := Callback{Code: "code", State: u.Query().Get("state"), Iss: fixtureIssuer}
	observed := 0
	session, err := f.client.CompleteLogin(context.Background(), login, callback, f.key, func(p LoginProgress) {
		observed++
		if p.Claims["sub"] != "user-1" {
			t.Error("unverified progress claims")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed != 1 || session.RefreshToken != "rotated-2" || session.Certificates[0].TenantUUID() != fixtureTenant || session.APIKey == "" || f.key.requests != 1 {
		t.Fatalf("incomplete sign-in: %+v", session)
	}
	claims, err := DecodeJWTClaims(session.APIKey)
	if err != nil || claims["aud"] != "https://cloud.example/api" {
		t.Fatal("persisted PKI token instead of Cloud token")
	}
}

func TestPortableLoginRejectsCallbackBeforeExchange(t *testing.T) {
	f := fixture(t)
	attempt := &Login{Options: LoginOptions{Issuer: fixtureIssuer}, state: "expected"}
	for _, cb := range []Callback{{State: "wrong", Code: "code"}, {State: "expected"}} {
		if _, err := f.client.CompleteLogin(context.Background(), attempt, cb, f.key, nil); err == nil {
			t.Fatal("invalid callback accepted")
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid callback spent a code")
	}
}

func TestCertificateRefreshRetainsRotationAfterPartialFailure(t *testing.T) {
	for _, stage := range []string{"jwks", "issue", "cloud-subject"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture(t)
			f.fail = stage
			s := fixtureSession()
			old := s.Certificates[0]
			if err := f.client.RefreshCertificate(context.Background(), s, f.key, f.key, false); err == nil {
				t.Fatal("expected refusal")
			}
			want := "rotated-1"
			if stage == "cloud-subject" {
				want = "rotated-2"
			}
			if s.RefreshToken != want {
				t.Fatalf("rotation lost: %q, want %q", s.RefreshToken, want)
			}
			if s.Certificates[0] != old {
				t.Fatal("partial failure replaced certificate")
			}
		})
	}
}

func TestCertificateRefreshRefusesTenantOrOperatorSwitch(t *testing.T) {
	for _, change := range []string{"tenant", "operator"} {
		f := fixture(t)
		s := fixtureSession()
		if change == "tenant" {
			f.key.tenant = "11111111-1111-1111-1111-111111111111"
		} else {
			f.key.subject = "other"
		}
		if err := f.client.RefreshCertificate(context.Background(), s, f.key, f.key, false); err == nil {
			t.Fatal("cross-identity certificate accepted")
		}
		if s.RefreshToken != "rotated-1" {
			t.Fatal("consumed refresh handle lost")
		}
	}
}

func TestRefreshAccessAndCertificateRespectCancellation(t *testing.T) {
	f := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.client.RefreshAccessToken(ctx, fixtureSession(), f.key); !errors.Is(err, context.Canceled) {
		t.Fatalf("access refresh: %v", err)
	}
	if err := f.client.RefreshCertificate(ctx, fixtureSession(), f.key, f.key, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("certificate refresh: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("canceled workflow sent a request")
	}
}

func TestTokenExchangeRetriesNonceOnceWithExactForm(t *testing.T) {
	key := testSigner{testKey(t)}
	attempt := 0
	client := &Client{HTTP: testHTTP(func(r *http.Request) (*http.Response, error) {
		attempt++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("refresh_token") != "single-family" || r.Form.Get("resource") != "resource" {
			t.Fatal("retry changed exchange")
		}
		proof, err := DecodeJWTClaims(r.Header.Get("DPoP"))
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			r := jsonResponse(400, map[string]string{"error": "use_dpop_nonce"})
			r.Header.Set("DPoP-Nonce", "nonce")
			return r, nil
		}
		if proof["nonce"] != "nonce" {
			t.Fatal("nonce missing on retry")
		}
		return jsonResponse(200, TokenResponse{AccessToken: "new"}), nil
	})}
	if _, err := client.RefreshToken(context.Background(), key, &ProviderMetadata{TokenEndpoint: "https://auth.example/token"}, "client", "single-family", "resource"); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 {
		t.Fatalf("attempts=%d", attempt)
	}
}
