package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Scopes requests the group membership used by Cloud at session establishment.
const Scopes = "openid email profile groups"

// ProviderMetadata is the OIDC wire payload used by sign-in and refresh.
type ProviderMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	GrantTypes            []string `json:"grant_types_supported"`
	ScopesSupported       []string `json:"scopes_supported"`
	RequirePAR            bool     `json:"require_pushed_authorization_requests"`
}

// TokenResponse is the OIDC wire payload used by sign-in and refresh.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

// IssuerRealm returns the realm label of an issuer URL.
func IssuerRealm(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil {
		return issuer
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := range parts {
		if parts[i] == "realms" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return issuer
}

// EffectiveLoginIssuer selects and validates the exact realm that may receive
// the authorization code. Realm-less starts require RFC 9207 `iss`; an explicit
// realm retains the compatibility fallback for servers that omit it.

// EffectiveLoginIssuer validates the RFC 9207 callback issuer against the configured authority.
func EffectiveLoginIssuer(requested, authorizationBase, callbackIss string) (string, error) {
	requested = strings.TrimSuffix(requested, "/")
	authority := requested
	if authority == "" {
		authority = strings.TrimSuffix(authorizationBase, "/")
		if callbackIss == "" {
			return "", fmt.Errorf("realm-less authorization response did not identify its issuer; refusing to exchange the code")
		}
	}
	if callbackIss == "" {
		return ValidateRealmIssuer(requested, requested)
	}
	return ValidateRealmIssuer(authority, callbackIss)
}

// ValidateRealmIssuer refuses noncanonical, system or foreign-authority realm URLs.
func ValidateRealmIssuer(authority, issuer string) (string, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	if !SameIssuerOrigin(issuer, authority) {
		return "", fmt.Errorf("callback issuer mismatch: %q is not on the configured authority %q; refusing to exchange the code", issuer, authority)
	}
	u, err := url.Parse(issuer)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("callback issuer mismatch: %q is not a canonical realm issuer", issuer)
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "realms" || parts[1] == "" || parts[1] == "system" {
		return "", fmt.Errorf("callback issuer mismatch: %q is not a canonical non-system realm issuer", issuer)
	}
	if decoded, err := url.PathUnescape(parts[1]); err != nil || decoded != parts[1] || strings.Contains(decoded, "/") {
		return "", fmt.Errorf("callback issuer mismatch: %q contains an invalid realm", issuer)
	}
	return issuer, nil
}

// SameIssuerOrigin reports whether two issuer URLs share scheme and host, i.e.
// name realms on the same wendy-auth deployment. The RFC 9207 issuer on an
// authorization response may switch realms (path) but must never move the code
// exchange to a different host — that would hand the authorization code to an
// unrelated issuer (the mix-up attack the check defends against).

// SameIssuerOrigin compares authority scheme and host.
func SameIssuerOrigin(a, b string) bool {
	ua, erra := url.Parse(a)
	ub, errb := url.Parse(b)
	if erra != nil || errb != nil || ua.Scheme == "" || ua.Host == "" {
		return false
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host
}

// CanonicalAuthorizationBase accepts HTTPS authorities or loopback HTTP for local hosts.
func CanonicalAuthorizationBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid wendy-auth authorization base %q", raw)
	}
	loopback := u.Hostname() == "localhost"
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", fmt.Errorf("invalid wendy-auth authorization base %q: want HTTPS or loopback HTTP", raw)
	}
	return u.String(), nil
}

// discoverOIDC fetches the realm's OIDC metadata.
//
// It deliberately checks that the advertised issuer matches the one requested:
// a discovery document that names a different issuer is either a
// misconfiguration or an attempt to redirect the client to another realm, and
// every downstream check (token `iss`, JWKS location) keys off this value.

// DecodeJWTClaims decodes a compact JWS payload without verifying its signature.
func DecodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a compact JWS (%d segments)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding token payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing token claims: %w", err)
	}
	return claims, nil
}

// ConfirmationThumbprint extracts cnf.jkt from decoded claims, if present.

// ConfirmationThumbprint reads the sender constraint from already verified claims.
func ConfirmationThumbprint(claims map[string]any) string {
	cnf, ok := claims["cnf"].(map[string]any)
	if !ok {
		return ""
	}
	jkt, _ := cnf["jkt"].(string)
	return jkt
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// BuildAuthorizeURL assembles a PKCE authorization query with percent-encoded scopes.
func BuildAuthorizeURL(meta *ProviderMetadata, clientID, redirectURI, challenge, state, nonce, resource, loginHint string) (string, error) {
	u, err := url.Parse(meta.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("parsing authorization_endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", Scopes)
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if resource != "" {
		// Carried on the authorization request as well as the token request:
		// RFC 8707 allows either, and sending it here lets the server refuse an
		// unknown resource before the user authenticates.
		q.Set("resource", resource)
	}
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	// url.Values.Encode() renders spaces as "+", which is correct only for an
	// application/x-www-form-urlencoded BODY. In a query string "+" is a
	// literal plus, and wendy-auth parses it strictly: `scope=openid+email`
	// arrives as the single unknown scope "openid+email" and the request is
	// rejected with invalid_scope. Percent-encoding is unambiguous in both
	// contexts, so rewrite the separators.
	//
	// Safe as a blanket replacement here: every other parameter is base64url
	// (which uses '-' and '_', never '+'), a loopback URI, or the login_hint
	// email, whose literal '+' Encode already escapes as %2B.
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String(), nil
}

// AudienceContains reports whether `aud` includes want. RFC 7519 §4.1.3 permits
// either a bare string or an array.

// AudienceContains checks string or array audience membership.
func AudienceContains(aud any, want string) bool {
	switch v := aud.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
