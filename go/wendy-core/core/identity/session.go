package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// DefaultIdentityResource is the audience pinned by pki-core.
const DefaultIdentityResource = "https://pki.wendy.sh/identity"

// DefaultIdentityEndpoint is the CLI's existing default identity endpoint.
const DefaultIdentityEndpoint = "https://identity.dev.pki.wendy.sh/v1/identity/certificate"

// Session is portable authentication state. Private keys and key references stay
// in the host. RefreshToken/APIKey are secrets: hosts persist them in secret
// storage and must never log or include them in errors.
type Session struct {
	Name                                      string
	CloudDashboard, CloudGRPC                 string
	OAuthIssuer, OAuthClientID, OAuthResource string
	PKIResource, PKIEndpoint                  string
	APIKey, RefreshToken, OAuthExpiresAt      string
	ServiceAccount                            string
	Certificates                              []Certificate
	LiteCertificate                           string
}

// RefreshCertificate rotates to the PKI audience, issues a certificate, then
// restores the Cloud audience. The host supplies both keys (same for primary,
// separate for Lite). Persist the mutated refresh token even if a later step
// fails: the previous token may already be consumed. Caller holds its refresh lock.
func (c *Client) RefreshCertificate(ctx context.Context, auth *Session, key platform.Signer, csrKey platform.IdentityKey, lite bool) error {
	refreshToken := auth.RefreshToken
	if refreshToken == "" {
		return fmt.Errorf("OAuth session has no refresh token; sign in again")
	}
	thumbprint, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		return fmt.Errorf("computing OAuth DPoP thumbprint: %w", err)
	}
	meta, err := c.Discover(ctx, auth.OAuthIssuer)
	if err != nil {
		return err
	}
	identityResource := auth.PKIResource
	if identityResource == "" {
		identityResource = DefaultIdentityResource
	}
	identityEndpoint := auth.PKIEndpoint
	if identityEndpoint == "" {
		identityEndpoint = DefaultIdentityEndpoint
	}

	identityToken, err := c.RefreshToken(
		ctx, key, meta, auth.OAuthClientID, refreshToken, identityResource,
	)
	if err != nil {
		return fmt.Errorf("obtaining pki-core identity token: %w", err)
	}
	if identityToken.RefreshToken == "" {
		return fmt.Errorf("wendy-auth did not rotate the refresh token for pki-core enrollment")
	}
	// The old handle has already been consumed. Retain the rotated one even if
	// a later validation or issuance step fails; refreshCerts persists this
	// mutation so the user's login is not stranded.
	auth.RefreshToken = identityToken.RefreshToken
	tokenVerifier, err := c.LoadTokenVerifier(ctx, meta)
	if err != nil {
		return err
	}
	claims, err := VerifyAccessToken(ctx, tokenVerifier, identityToken.AccessToken, identityResource, thumbprint)
	if err != nil {
		return fmt.Errorf("verifying pki-core identity token: %w", err)
	}
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return fmt.Errorf("pki-core identity token carries no sub claim")
	}
	if len(auth.Certificates) == 0 || auth.Certificates[0].TenantUUID() == "" {
		return fmt.Errorf("stored OAuth session has no authoritative tenant certificate; sign in again")
	}
	expectedTenant := auth.Certificates[0].TenantUUID()

	certInfo, err := c.RequestIdentityCertificate(ctx, identityEndpoint, csrKey, key, identityToken.AccessToken, expectedTenant, subject)
	if err != nil {
		return err
	}
	cloudToken, err := c.RefreshToken(
		ctx, key, meta, auth.OAuthClientID, identityToken.RefreshToken, auth.OAuthResource,
	)
	if err != nil {
		return fmt.Errorf("restoring Cloud API token after certificate refresh: %w", err)
	}
	if cloudToken.RefreshToken != "" {
		auth.RefreshToken = cloudToken.RefreshToken
	}
	cloudClaims, err := VerifyAccessToken(ctx, tokenVerifier, cloudToken.AccessToken, auth.OAuthResource, thumbprint)
	if err != nil {
		return fmt.Errorf("verifying refreshed Cloud API token: %w", err)
	}
	if cloudSubject, _ := cloudClaims["sub"].(string); cloudSubject != subject {
		return fmt.Errorf("refreshed Cloud API token identifies a different subject")
	}
	auth.APIKey = cloudToken.AccessToken
	auth.OAuthExpiresAt = time.Now().Add(time.Duration(cloudToken.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	auth.PKIResource = identityResource
	auth.PKIEndpoint = identityEndpoint
	if lite {
		auth.LiteCertificate = certInfo.PemCertificate + certInfo.PemCertificateChain
	} else {
		auth.Certificates = []Certificate{certInfo}
	}
	return nil
}

// AccessTokenFreshFor reports whether a session outlasts the requested margin.
func AccessTokenFreshFor(auth *Session, d time.Duration) bool {
	expiresAt, err := time.Parse(time.RFC3339, auth.OAuthExpiresAt)
	return err == nil && time.Until(expiresAt) > d
}

// RefreshAccessToken refreshes an expired session using host-owned signing.
// The caller reloads state under its refresh lock before calling and persists
// any resulting state. No secret store or network connection is opened by Core.
func (c *Client) RefreshAccessToken(ctx context.Context, auth *Session, key platform.Signer) error {
	if AccessTokenFreshFor(auth, 90*time.Second) {
		return nil
	}
	if auth.ServiceAccount != "" {
		return c.RemintServiceAccount(ctx, auth, key)
	}
	refreshToken := auth.RefreshToken
	if refreshToken == "" {
		return fmt.Errorf("OAuth session expired and has no refresh token; run 'wendy auth login --email <email>' again")
	}
	meta, err := c.Discover(ctx, auth.OAuthIssuer)
	if err != nil {
		return err
	}
	token, err := c.RefreshToken(ctx, key, meta, auth.OAuthClientID, refreshToken, auth.OAuthResource)
	if err != nil {
		return fmt.Errorf("refreshing OAuth session: %w", err)
	}
	thumbprint, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		return fmt.Errorf("computing OAuth DPoP thumbprint: %w", err)
	}
	tokenVerifier, err := c.LoadTokenVerifier(ctx, meta)
	if err != nil {
		return err
	}
	if _, err := VerifyAccessToken(ctx, tokenVerifier, token.AccessToken, auth.OAuthResource, thumbprint); err != nil {
		return fmt.Errorf("verifying refreshed access token: %w", err)
	}
	auth.APIKey = token.AccessToken
	if token.RefreshToken != "" {
		auth.RefreshToken = token.RefreshToken
	}
	auth.OAuthExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	return nil
}
