package commands

// OpenID Connect login against wendy-auth for the Wendy Cloud API and
// pki-core. The native client uses authorization code + PKCE and binds both
// resource tokens and the operator CSR to one local DPoP key.

import (
	"context"
	"crypto"
	"fmt"
	"net"
	"net/http"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy-go/identity"
	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"

	"github.com/wendylabsinc/wendy/go/internal/cli/cloudrequest"
)

// oidcScopes: `groups` is read once at session establishment and drives cloud's
// live authorization decisions. The realm must advertise it.
const oidcScopes = identity.Scopes

// oidcLoginOptions configures a single login attempt.
type oidcLoginOptions struct {
	// Issuer is an explicitly selected realm issuer URL. When it is empty,
	// AuthorizationBase starts the realm-less universal browser flow and the
	// exact realm issuer must arrive in the RFC 9207 callback.
	Issuer            string
	AuthorizationBase string
	// LoginHint prefills the email on the universal page (OIDC login_hint)
	// when --email discovery found no pinned realm.
	LoginHint string
	// ClientID is the public client registered in that realm. Public + PKCE,
	// token_endpoint_auth_method=none: a CLI cannot keep a secret.
	ClientID string
	// CloudResource is the audience used for the access token persisted for
	// ordinary Cloud API calls. IdentityResource is the pki-core audience used
	// only during certificate bootstrap.
	CloudResource    string
	IdentityResource string
	// IdentityEndpoint is pki-core's operator-identity CSR endpoint. Its exact
	// canonical URL is bound into the resource-request DPoP proof.
	IdentityEndpoint string
	// CloudURL and CloudGRPC identify the API environment this session targets.
	CloudURL  string
	CloudGRPC string
	// PrintClaims dumps the decoded access-token payload after exchange.
	PrintClaims bool
}

// oidcProviderMetadata is the subset of OIDC discovery this flow needs.
type oidcProviderMetadata = identity.ProviderMetadata

// oidcTokenResponse is the token endpoint's success payload.
type oidcTokenResponse = identity.TokenResponse

// discoverOIDCIssuerFn is indirected so command tests can verify target routing
// without contacting a live wendy-auth deployment.
var discoverOIDCIssuerFn = discoverOIDCIssuer

// discoverOIDCIssuer uses wendy-auth's identifier-first API to route an email
// address to its home realm without exposing or requiring an organization list.
// It returns "" when no realm is pinned for the email: wendy-auth answers
// "unknown" (older servers name the system realm), and the caller falls through
// to the realm-less universal flow. The system realm is never a login target.
func discoverOIDCIssuer(ctx context.Context, base, email string) (string, error) {
	return identityhost.Client(http.DefaultClient).DiscoverIssuer(ctx, base, email)
}

func issuerRealm(issuer string) string { return identity.IssuerRealm(issuer) }

// effectiveLoginIssuer selects and validates the exact realm that may receive
// the authorization code. Realm-less starts require RFC 9207 `iss`; an explicit
// realm retains the compatibility fallback for servers that omit it.
func effectiveLoginIssuer(requested, base, callback string) (string, error) {
	return identity.EffectiveLoginIssuer(requested, base, callback)
}

func validateRealmIssuer(authority, issuer string) (string, error) {
	return identity.ValidateRealmIssuer(authority, issuer)
}

// sameIssuerOrigin reports whether two issuer URLs share scheme and host, i.e.
// name realms on the same wendy-auth deployment. The RFC 9207 issuer on an
// authorization response may switch realms (path) but must never move the code
// exchange to a different host — that would hand the authorization code to an
// unrelated issuer (the mix-up attack the check defends against).
func sameIssuerOrigin(a, b string) bool { return identity.SameIssuerOrigin(a, b) }

func canonicalAuthorizationBase(raw string) (string, error) {
	return identity.CanonicalAuthorizationBase(raw)
}

// discoverOIDC fetches the realm's OIDC metadata.
//
// It deliberately checks that the advertised issuer matches the one requested:
// a discovery document that names a different issuer is either a
// misconfiguration or an attempt to redirect the client to another realm, and
// every downstream check (token `iss`, JWKS location) keys off this value.
func discoverOIDC(ctx context.Context, issuer string) (*oidcProviderMetadata, error) {
	return identityhost.Client(http.DefaultClient).Discover(ctx, issuer)
}

// newPKCEVerifier returns a high-entropy code_verifier and its S256 challenge.
func newPKCEVerifier() (string, string, error) {
	return identityhost.Client(http.DefaultClient).NewPKCEVerifier(context.Background())
}

// randomURLSafe returns n bytes of entropy, base64url-encoded. Used for `state`
// and DPoP `jti`.
func randomURLSafe(n int) (string, error) {
	return identityhost.Client(http.DefaultClient).RandomURLSafe(context.Background(), n)
}

// The DPoP proof builder and the operator JWK/JWS helpers live in the shared
// cloudrequest package (WDY-3107) so the gRPC broker-path interceptor and the
// MCP tools path reuse the one signer. These thin wrappers keep the established
// OIDC login/refresh/enroll call sites and their tests unchanged.

func operatorPublicJWK(signer crypto.Signer) (map[string]string, string, error) {
	return cloudrequest.OperatorPublicJWK(signer)
}

func operatorJWKThumbprint(signer crypto.Signer) (string, error) {
	return cloudrequest.OperatorJWKThumbprint(signer)
}

func newDPoPProof(key crypto.Signer, htm, htu, nonce string) (string, error) {
	return cloudrequest.NewDPoPProof(key, htm, htu, nonce)
}

func newDPoPAccessProof(key crypto.Signer, htm, htu, accessToken string) (string, error) {
	return cloudrequest.NewDPoPAccessProof(key, htm, htu, accessToken)
}

func canonicalHTU(raw string) (string, error) {
	return cloudrequest.CanonicalHTU(raw)
}

// decodeJWTClaims returns the decoded payload of a JWS compact token.
//
// This does NOT verify the signature: it exists so the CLI can inspect what it
// received and assert the cnf.jkt binding locally. Cloud verifies the token
// signature, issuer, and audience before authorizing an API request.
func decodeJWTClaims(raw string) (map[string]any, error) { return identity.DecodeJWTClaims(raw) }

// confirmationThumbprint extracts cnf.jkt from decoded claims, if present.
func confirmationThumbprint(claims map[string]any) string {
	return identity.ConfirmationThumbprint(claims)
}

const (
	oidcCallbackAddr = "127.0.0.1:8765"
	oidcRedirectURI  = "http://127.0.0.1:8765/callback"
)

// startLoopbackListener binds the static OIDC callback port and returns its
// registered redirect URI.
//
// Loopback rather than the device-code grant: wendy-auth does not advertise
// urn:ietf:params:oauth:grant-type:device_code, and the existing
// `wendy auth login` already uses a loopback callback, so this keeps one shape.
func startLoopbackListener() (net.Listener, string, error) {
	listener, err := net.Listen("tcp", oidcCallbackAddr)
	if err != nil {
		return nil, "", fmt.Errorf("binding OIDC callback server to %s: %w", oidcCallbackAddr, err)
	}
	return listener, oidcRedirectURI, nil
}

// exchangeCodeForToken performs the authorization_code exchange with a DPoP
// proof and the RFC 8707 resource indicator.
//
// If the server answers `use_dpop_nonce`, the proof is rebuilt once with the
// supplied nonce and retried — RFC 9449 §8 requires clients to handle this, and
// wendy-auth can be configured to demand it
// (WENDY_AUTH_DPOP_REQUIRE_NONCE).
func exchangeCodeForToken(ctx context.Context, key crypto.Signer, meta *oidcProviderMetadata, clientID, code, verifier, redirectURI, resource string) (*oidcTokenResponse, error) {
	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return nil, err
	}
	return identityhost.Client(http.DefaultClient).ExchangeCode(ctx, signer, meta, clientID, code, verifier, redirectURI, resource)
}

func refreshOIDCToken(ctx context.Context, key crypto.Signer, meta *oidcProviderMetadata, clientID, refreshToken, resource string) (*oidcTokenResponse, error) {
	signer, err := identityhost.OperatorKey(key)
	if err != nil {
		return nil, err
	}
	return identityhost.Client(http.DefaultClient).RefreshToken(ctx, signer, meta, clientID, refreshToken, resource)
}

func parseTokenResponse(response *http.Response) (*oidcTokenResponse, error) {
	return identity.ParseTokenResponse(&wendy.HTTPResponse{StatusCode: response.StatusCode, Headers: wendy.HTTPHeaders(response.Header), Body: response.Body})
}
