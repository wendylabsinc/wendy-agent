package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

const jwtBearerGrantType = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// MintServiceAccountToken exchanges a host-signed JWT assertion at the realm endpoint.
func (c *Client) MintServiceAccountToken(ctx context.Context, key platform.Signer, issuer, subject, resource string) (*TokenResponse, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	meta, err := c.Discover(ctx, issuer)
	if err != nil {
		return nil, err
	}
	_, alg, err := OperatorPublicJWK(ctx, key)
	if err != nil {
		return nil, err
	}
	kid, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		return nil, err
	}
	jti, err := RandomURLSafe(16)
	if err != nil {
		return nil, fmt.Errorf("generating assertion jti: %w", err)
	}
	now := time.Now()
	assertion, err := SignJWS(ctx, key,
		map[string]any{"alg": alg, "kid": kid},
		map[string]any{
			"iss": subject, "sub": subject, "aud": meta.TokenEndpoint, "jti": jti,
			"iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
		})
	if err != nil {
		return nil, fmt.Errorf("signing service-account assertion: %w", err)
	}
	form := url.Values{}
	form.Set("grant_type", jwtBearerGrantType)
	form.Set("assertion", assertion)
	if resource != "" {
		form.Set("resource", resource)
	}
	token, err := c.postForToken(ctx, meta.TokenEndpoint, "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("minting service-account token: %w", err)
	}
	if err := CheckServiceAccountToken(token, kid, issuer, resource); err != nil {
		return nil, err
	}
	return token, nil
}

func (c *Client) postForToken(ctx context.Context, endpoint, contentType string, body []byte) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return ParseTokenResponse(resp)
}

// CheckServiceAccountToken preserves the headless token issuer, audience and key-binding checks.
func CheckServiceAccountToken(token *TokenResponse, kid, issuer, resource string) error {
	claims, err := DecodeJWTClaims(token.AccessToken)
	if err != nil {
		return fmt.Errorf("inspecting service-account access token: %w", err)
	}
	if ConfirmationThumbprint(claims) != kid {
		return fmt.Errorf("service-account access token is not bound to this key")
	}
	if iss, _ := claims["iss"].(string); iss != issuer {
		return fmt.Errorf("service-account access token issuer %q does not match %q", iss, issuer)
	}
	if resource != "" && !AudienceContains(claims["aud"], resource) {
		return fmt.Errorf("service-account access token audience does not include %s", resource)
	}
	return nil
}

// TokenExpiry formats the token lifetime as the existing RFC3339 session expiry.
func TokenExpiry(token *TokenResponse) string {
	return time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
}

// RemintServiceAccount replaces an expired headless access token. The caller
// holds its refresh lock and persists the session after success.
func (c *Client) RemintServiceAccount(ctx context.Context, auth *Session, key platform.Signer) error {
	token, err := c.MintServiceAccountToken(ctx, key, auth.OAuthIssuer, auth.ServiceAccount, auth.OAuthResource)
	if err != nil {
		return err
	}
	auth.APIKey = token.AccessToken
	auth.OAuthExpiresAt = TokenExpiry(token)
	return nil
}

// EnrollServiceAccount registers a host-generated key with a one-time secret.
// The host claims the key-storage destination before spending the secret and
// persists its own key after success; Core returns only the account subject.
func (c *Client) EnrollServiceAccount(ctx context.Context, key platform.Signer, issuer, secret string) (string, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	jwk, _, err := OperatorPublicJWK(ctx, key)
	if err != nil {
		return "", err
	}
	kid, err := OperatorJWKThumbprint(ctx, key)
	if err != nil {
		return "", err
	}
	endpoint := issuer + "/service-accounts/enroll"
	pop, err := c.DPoPProof(ctx, key, http.MethodPost, endpoint, "")
	if err != nil {
		return "", fmt.Errorf("building enrollment proof: %w", err)
	}
	body, err := json.Marshal(map[string]any{"enrollment_secret": secret, "public_jwk": jwk, "pop": pop})
	if err != nil {
		return "", err
	}
	token, err := c.postForToken(ctx, endpoint, "application/json", body)
	if err != nil {
		return "", fmt.Errorf("enrolling service account: %w", err)
	}
	if err := CheckServiceAccountToken(token, kid, issuer, ""); err != nil {
		return "", err
	}
	claims, err := DecodeJWTClaims(token.AccessToken)
	if err != nil {
		return "", err
	}
	subject, _ := claims["sub"].(string)
	if subject == "" {
		return "", fmt.Errorf("enrollment access token carries no sub claim")
	}
	return subject, nil
}
