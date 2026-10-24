package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// DiscoverIssuer uses identifier-first discovery; an empty result selects universal sign-in.
func (c *Client) DiscoverIssuer(ctx context.Context, authBase, email string) (string, error) {
	if strings.TrimSpace(email) == "" {
		return "", fmt.Errorf("--email is required when --issuer is not set")
	}
	authBase = strings.TrimSuffix(authBase, "/")
	body, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		return "", fmt.Errorf("encoding realm discovery request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authBase+"/api/login/realm", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building realm discovery request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("discovering organization: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("organization discovery returned %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
	}
	var result struct {
		LoginURL string `json:"loginURL"`
		Realm    string `json:"realm"`
		State    string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding organization discovery response: %w", err)
	}
	if result.State == "unknown" {
		return "", nil
	}
	slug := result.Realm
	if slug == "" && result.LoginURL != "" {
		u, err := url.Parse(result.LoginURL)
		if err != nil {
			return "", fmt.Errorf("parsing organization login URL: %w", err)
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 || parts[0] != "realms" || parts[1] == "" {
			return "", fmt.Errorf("organization discovery returned an invalid login URL")
		}
		slug = parts[1]
	}
	if slug == "" || slug == "system" {
		return "", nil
	}
	return authBase + "/realms/" + url.PathEscape(slug), nil
}

// Discover loads realm metadata and enforces issuer, endpoints and PKCE S256 support.
func (c *Client) Discover(ctx context.Context, issuer string) (*ProviderMetadata, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	discoveryURL := issuer + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building discovery request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", discoveryURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("discovery at %s returned %d: %s", discoveryURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var meta ProviderMetadata
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, fmt.Errorf("decoding discovery document: %w", err)
	}
	if meta.Issuer != issuer {
		return nil, fmt.Errorf("discovery issuer mismatch: requested %q, document declares %q", issuer, meta.Issuer)
	}
	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("discovery document missing authorization_endpoint or token_endpoint")
	}
	if meta.RequirePAR {
		return nil, fmt.Errorf("issuer requires pushed authorization requests, which the realm-less flow does not support")
	}
	// PKCE S256 is mandatory on this authorization server; fail loudly rather
	// than silently downgrading to a flow it will reject anyway.
	if len(meta.CodeChallengeMethods) > 0 && !containsString(meta.CodeChallengeMethods, "S256") {
		return nil, fmt.Errorf("realm does not advertise PKCE S256 (got %v)", meta.CodeChallengeMethods)
	}
	return &meta, nil
}

// base64URL encodes without padding, as every JOSE/OAuth structure requires.

// ExchangeCode exchanges an authorization code using a host-owned DPoP key.
func (c *Client) ExchangeCode(
	ctx context.Context,
	key platform.Signer,
	meta *ProviderMetadata,
	clientID, code, verifier, redirectURI, resource string,
) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	if resource != "" {
		form.Set("resource", resource)
	}
	return c.exchangeDPoPToken(ctx, key, meta.TokenEndpoint, form)
}

// RefreshToken rotates a refresh token with an explicit resource audience.
func (c *Client) RefreshToken(
	ctx context.Context,
	key platform.Signer,
	meta *ProviderMetadata,
	clientID, refreshToken, resource string,
) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	if resource != "" {
		form.Set("resource", resource)
	}
	return c.exchangeDPoPToken(ctx, key, meta.TokenEndpoint, form)
}

func (c *Client) exchangeDPoPToken(
	ctx context.Context,
	key platform.Signer,
	tokenEndpoint string,
	form url.Values,
) (*TokenResponse, error) {
	htu, err := CanonicalHTU(tokenEndpoint)
	if err != nil {
		return nil, err
	}

	doRequest := func(nonce string) (*http.Response, error) {
		proof, err := c.DPoPProof(ctx, key, http.MethodPost, htu, nonce)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("building token request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("DPoP", proof)
		return c.HTTP.Do(req)
	}

	resp, err := doRequest("")
	if err != nil {
		return nil, fmt.Errorf("calling token endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var oauthErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oauthErr)
		if oauthErr.Error == "use_dpop_nonce" {
			nonce := resp.Header.Get("DPoP-Nonce")
			if nonce == "" {
				return nil, fmt.Errorf("server demanded a DPoP nonce but sent no DPoP-Nonce header")
			}
			_ = resp.Body.Close()
			retry, err := doRequest(nonce)
			if err != nil {
				return nil, fmt.Errorf("retrying token request with nonce: %w", err)
			}
			defer func() { _ = retry.Body.Close() }()
			return ParseTokenResponse(retry)
		}
		return nil, fmt.Errorf("token endpoint returned 400: %s", strings.TrimSpace(string(body)))
	}

	return ParseTokenResponse(resp)
}

// ParseTokenResponse decodes the existing OAuth token endpoint response.
func ParseTokenResponse(resp *http.Response) (*TokenResponse, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding token response: %w", err)
	}
	if out.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access_token")
	}
	return &out, nil
}
