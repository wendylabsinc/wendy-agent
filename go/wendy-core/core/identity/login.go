package identity

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// LoginOptions selects the authority, client and resource audiences.
type LoginOptions struct {
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
}

// Login holds one browser authorization attempt. Keep it private to the host's
// login operation; its verifier/state/nonce are not reusable between attempts.
type Login struct {
	Options                             LoginOptions
	metadata                            *ProviderMetadata
	verifier, nonce, state, redirectURI string
}

// Callback carries the authorization response received by the host's listener.
type Callback struct{ Code, State, Iss string }

// LoginProgress reports verified claims before certificate issuance, allowing
// the CLI to preserve its established messages and optional claim display.
// Tokens are credentials and must not be logged by default.
type LoginProgress struct {
	Claims map[string]any
	Token  *TokenResponse
}

// PrepareLogin settles the authorization authority before the host generates
// an operator key or opens a browser, preserving the CLI's failure ordering.
func (c *Client) PrepareLogin(ctx context.Context, opts LoginOptions) (*Login, error) {
	if opts.Issuer == "" && opts.AuthorizationBase == "" {
		return nil, fmt.Errorf("an OIDC issuer or authorization base is required")
	}
	if opts.ClientID == "" {
		return nil, fmt.Errorf("--client-id is required")
	}
	if opts.CloudResource == "" {
		return nil, fmt.Errorf("--resource is required for OIDC login")
	}
	if opts.IdentityResource == "" {
		return nil, fmt.Errorf("--pki-resource is required for OIDC login")
	}
	if opts.IdentityEndpoint == "" {
		return nil, fmt.Errorf("--pki-identity-endpoint is required for OIDC login")
	}
	if opts.CloudGRPC == "" {
		return nil, fmt.Errorf("--cloud-grpc is required for OIDC login")
	}

	// Step 1: explicit realms use discovery immediately. Realm-less login starts
	// only at the configured authority's fixed global endpoint; discovery waits
	// until the callback identifies the selected realm.
	var meta *ProviderMetadata
	var err error
	if opts.Issuer != "" {
		issuer, err := ValidateRealmIssuer(opts.Issuer, opts.Issuer)
		if err != nil {
			return nil, fmt.Errorf("realm issuer %q is not a canonical non-system realm issuer", opts.Issuer)
		}
		opts.Issuer = issuer
		meta, err = c.Discover(ctx, opts.Issuer)
		if err != nil {
			return nil, err
		}
	} else {
		opts.AuthorizationBase, err = CanonicalAuthorizationBase(opts.AuthorizationBase)
		if err != nil {
			return nil, err
		}
		meta = &ProviderMetadata{AuthorizationEndpoint: opts.AuthorizationBase + "/authorize"}
	}

	return &Login{Options: opts, metadata: meta}, nil
}

// BeginAuthorization creates PKCE/state/nonce and the browser URL. The host
// has already opened its callback listener and supplies the registered URI.
func (l *Login) BeginAuthorization(redirectURI string) (string, error) {
	verifier, challenge, err := NewPKCEVerifier()
	if err != nil {
		return "", err
	}
	state, err := RandomURLSafe(16)
	if err != nil {
		return "", fmt.Errorf("generating state: %w", err)
	}
	nonce, err := RandomURLSafe(16)
	if err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	l.verifier = verifier
	l.state = state
	l.nonce = nonce
	l.redirectURI = redirectURI
	return BuildAuthorizeURL(l.metadata, l.Options.ClientID, redirectURI, challenge, state, nonce, l.Options.IdentityResource, l.Options.LoginHint)
}

// State is the expected callback state, used by the host to reject unrelated
// loopback requests before completing the pending login.
func (l *Login) State() string { return l.state }

// CompleteLogin exchanges the callback code, checks the signed identity and key
// binding, issues an operator certificate directly at PKI and rotates to the
// Cloud resource. Keys, browser UI and persistence remain with the host.
func (c *Client) CompleteLogin(ctx context.Context, l *Login, result Callback, key platform.IdentityKey, observer func(LoginProgress)) (*Session, error) {
	if l.state == "" || subtle.ConstantTimeCompare([]byte(result.State), []byte(l.state)) != 1 {
		return nil, fmt.Errorf("OIDC callback state mismatch")
	}
	if result.Code == "" {
		return nil, fmt.Errorf("OIDC callback contained no authorization code")
	}
	opts, meta := l.Options, l.metadata
	verifier, nonce, redirectURI := l.verifier, l.nonce, l.redirectURI
	cloudResource, identityResource := opts.CloudResource, opts.IdentityResource
	thumbprint, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("computing key thumbprint: %w", err)
	}
	effectiveIssuer, err := EffectiveLoginIssuer(opts.Issuer, opts.AuthorizationBase, result.Iss)
	if err != nil {
		return nil, err
	}
	if opts.Issuer == "" || effectiveIssuer != strings.TrimSuffix(opts.Issuer, "/") {
		// A realm-less start or browser realm switch discovers only after the
		// callback issuer passed the authority and canonical-path checks.
		meta, err = c.Discover(ctx, effectiveIssuer)
		if err != nil {
			return nil, err
		}
	}

	// Step 6: exchange the code, DPoP-bound to the key from step 1.
	identityToken, err := c.ExchangeCode(ctx, key, meta, opts.ClientID, result.Code, verifier, redirectURI, identityResource)
	if err != nil {
		return nil, err
	}

	// Step 7: verify both signed OIDC identity and sender-constrained access.
	// The nonce binds the ID token to this browser attempt; the access-token
	// signature makes cnf, audience, issuer, and subject safe to inspect locally.
	tokenVerifier, err := c.LoadTokenVerifier(ctx, meta)
	if err != nil {
		return nil, err
	}
	subject, err := VerifyIDToken(ctx, tokenVerifier, identityToken, opts.ClientID, nonce)
	if err != nil {
		return nil, err
	}
	claims, err := VerifyAccessToken(ctx, tokenVerifier, identityToken.AccessToken, identityResource, thumbprint)
	if err != nil {
		return nil, fmt.Errorf("verifying pki-core identity access token: %w", err)
	}
	if accessSubject, _ := claims["sub"].(string); accessSubject != subject {
		return nil, fmt.Errorf("ID token and access token identify different subjects")
	}
	if observer != nil {
		observer(LoginProgress{Claims: claims, Token: identityToken})
	}
	if identityToken.RefreshToken == "" {
		return nil, fmt.Errorf("wendy-auth returned no refresh token; cannot obtain a separate Cloud API token after PKI enrollment")
	}

	// Step 8: create a PKCS#10 request with the same key used for DPoP and ask
	// pki-core directly. pki-core enforces the three-way binding between this
	// key, token cnf.jkt, and the proof's embedded JWK.
	certInfo, err := c.RequestIdentityCertificate(ctx, opts.IdentityEndpoint, key, key, identityToken.AccessToken, "", subject)
	if err != nil {
		return nil, err
	}

	// Step 9: wendy-auth currently issues one resource audience at a time.
	// Rotate the same sender-constrained refresh-token family from the PKI
	// audience to the Cloud API audience, and persist only this API token.
	cloudToken, err := c.RefreshToken(
		ctx, key, meta, opts.ClientID, identityToken.RefreshToken, cloudResource,
	)
	if err != nil {
		return nil, fmt.Errorf("obtaining Cloud API token after certificate enrollment: %w", err)
	}
	cloudClaims, err := VerifyAccessToken(ctx, tokenVerifier, cloudToken.AccessToken, cloudResource, thumbprint)
	if err != nil {
		return nil, fmt.Errorf("verifying Cloud API access token: %w", err)
	}
	if cloudSubject, _ := cloudClaims["sub"].(string); cloudSubject != subject {
		return nil, fmt.Errorf("Cloud API token identifies a different subject")
	}
	refreshToken := cloudToken.RefreshToken
	if refreshToken == "" {
		refreshToken = identityToken.RefreshToken
	}

	return &Session{
		CloudDashboard: opts.CloudURL, CloudGRPC: opts.CloudGRPC,
		APIKey: cloudToken.AccessToken, OAuthIssuer: effectiveIssuer,
		OAuthClientID: opts.ClientID, OAuthResource: cloudResource,
		PKIResource: identityResource, PKIEndpoint: opts.IdentityEndpoint,
		OAuthExpiresAt: TokenExpiry(cloudToken), RefreshToken: refreshToken,
		Certificates: []Certificate{certInfo},
	}, nil
}
