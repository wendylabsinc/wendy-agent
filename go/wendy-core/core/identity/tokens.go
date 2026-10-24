package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

// JWK is a public JSON Web Key.
type JWK = map[string]string

// TokenVerifier holds public issuer keys and a host signature verifier.
type TokenVerifier struct {
	issuer   string
	keys     []JWK
	verifier platform.SignatureVerifier
}

// VerifiedJWT contains claims accepted after signature and issuer/audience/time checks.
type VerifiedJWT struct {
	Claims    map[string]any
	Algorithm string
}

// LoadTokenVerifier loads issuer JWKS through the host HTTP transport.
func (c *Client) LoadTokenVerifier(ctx context.Context, meta *ProviderMetadata) (*TokenVerifier, error) {
	if meta == nil || meta.Issuer == "" || meta.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC metadata does not identify issuer signing keys")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.JWKSURI, nil)
	if err != nil {
		return nil, fmt.Errorf("building JWKS request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching issuer signing keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("issuer JWKS returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("reading issuer JWKS: %w", err)
	}
	if len(body) > 1<<20 {
		return nil, fmt.Errorf("issuer JWKS exceeds 1 MiB")
	}
	var document struct {
		Keys []JWK `json:"keys"`
	}
	if err := json.Unmarshal(body, &document); err != nil || len(document.Keys) == 0 {
		return nil, fmt.Errorf("issuer JWKS contains no usable keys")
	}
	return &TokenVerifier{issuer: meta.Issuer, keys: document.Keys, verifier: c.Verifier}, nil
}

// Verify checks the signature through the host before validating token claims.
func (v *TokenVerifier) Verify(ctx context.Context, raw, audience string) (*VerifiedJWT, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("token is not a compact JWS")
	}
	decode := base64.RawURLEncoding.DecodeString
	headerBytes, err := decode(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decoding token header: %w", err)
	}
	var header struct {
		Alg  string   `json:"alg"`
		Kid  string   `json:"kid"`
		Crit []string `json:"crit"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parsing token header: %w", err)
	}
	if header.Kid == "" || len(header.Crit) != 0 || (header.Alg != "ES256" && header.Alg != "ML-DSA-65") {
		return nil, fmt.Errorf("token uses an unsupported JOSE header")
	}
	signature, err := decode(parts[2])
	if err != nil {
		return nil, fmt.Errorf("decoding token signature: %w", err)
	}
	message := []byte(parts[0] + "." + parts[1])
	verified := false
	for _, key := range v.keys {
		if key["kid"] != header.Kid || key["alg"] != header.Alg {
			continue
		}
		if v.verifier == nil {
			return nil, fmt.Errorf("token signature verifier is required")
		}
		verifyErr := v.verifier.VerifySignature(ctx, key, message, signature)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		verified = verifyErr == nil
		if verified {
			break
		}
	}
	if !verified {
		return nil, fmt.Errorf("token signature verification failed")
	}
	payload, err := decode(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding token payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing token claims: %w", err)
	}
	if issuer, _ := claims["iss"].(string); issuer != v.issuer {
		return nil, fmt.Errorf("token issuer %q does not match %q", issuer, v.issuer)
	}
	if !AudienceContains(claims["aud"], audience) {
		return nil, fmt.Errorf("token audience does not include %s", audience)
	}
	now := float64(time.Now().Unix())
	expires, ok := claims["exp"].(float64)
	if !ok || expires <= now {
		return nil, fmt.Errorf("token is expired or carries no valid exp claim")
	}
	if notBefore, ok := claims["nbf"].(float64); ok && notBefore > now+60 {
		return nil, fmt.Errorf("token is not yet valid")
	}
	return &VerifiedJWT{Claims: claims, Algorithm: header.Alg}, nil
}

// VerifyIDToken checks identity, nonce and the supported access-token hash binding.
func VerifyIDToken(ctx context.Context, verifier *TokenVerifier, token *TokenResponse, clientID, nonce string) (string, error) {
	if token.IDToken == "" {
		return "", fmt.Errorf("token response contained no id_token")
	}
	verified, err := verifier.Verify(ctx, token.IDToken, clientID)
	if err != nil {
		return "", fmt.Errorf("verifying ID token: %w", err)
	}
	gotNonce, _ := verified.Claims["nonce"].(string)
	if nonce == "" || gotNonce != nonce {
		return "", fmt.Errorf("ID token nonce does not match the authorization request")
	}
	subject, _ := verified.Claims["sub"].(string)
	if subject == "" {
		return "", fmt.Errorf("ID token carries no sub claim")
	}
	if atHash, _ := verified.Claims["at_hash"].(string); atHash != "" {
		if verified.Algorithm != "ES256" {
			return "", fmt.Errorf("ID token carries at_hash for unsupported algorithm %s", verified.Algorithm)
		}
		digest := sha256.Sum256([]byte(token.AccessToken))
		want := base64.RawURLEncoding.EncodeToString(digest[:len(digest)/2])
		if atHash != want {
			return "", fmt.Errorf("ID token at_hash does not match the access token")
		}
	}
	return subject, nil
}

// VerifyAccessToken checks signed claims, sender constraint and subject.
func VerifyAccessToken(ctx context.Context, verifier *TokenVerifier, raw, audience, thumbprint string) (map[string]any, error) {
	verified, err := verifier.Verify(ctx, raw, audience)
	if err != nil {
		return nil, err
	}
	if got := ConfirmationThumbprint(verified.Claims); got != thumbprint {
		return nil, fmt.Errorf("access token is not bound to the expected DPoP key")
	}
	if subject, _ := verified.Claims["sub"].(string); subject == "" {
		return nil, fmt.Errorf("access token carries no sub claim")
	}
	return verified.Claims, nil
}
