package identity

import (
	"context"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
)

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// NewPKCEVerifier returns a high-entropy RFC 7636 verifier and S256 challenge.
func (c *Client) NewPKCEVerifier(ctx context.Context) (verifier, challenge string, err error) {
	raw, err := c.randomBytes(ctx, 32)
	if err != nil {
		return "", "", fmt.Errorf("generating PKCE verifier: %w", err)
	}
	verifier = base64URL(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64URL(sum[:]), nil
}

// RandomURLSafe returns n bytes of entropy as unpadded base64url.
func (c *Client) RandomURLSafe(ctx context.Context, n int) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("invalid entropy length")
	}
	raw, err := c.randomBytes(ctx, n)
	if err != nil {
		return "", err
	}
	return base64URL(raw), nil
}

// OperatorPublicJWK encodes an ML-DSA-65 host public key as an RFC 9964 AKP JWK.
// The host supplies DER SubjectPublicKeyInfo; Core only decodes its structure.
func OperatorPublicJWK(ctx context.Context, key platform.Signer) (map[string]string, string, error) {
	if key.Algorithm() != platform.MLDSA65 {
		return nil, "", fmt.Errorf("Wendy requires an ML-DSA-65 operator credential; re-run 'wendy auth login'")
	}
	der, err := key.PublicKey(ctx)
	if err != nil {
		return nil, "", err
	}
	var spki struct {
		Algorithm struct {
			OID        asn1.ObjectIdentifier
			Parameters asn1.RawValue `asn1:"optional"`
		}
		PublicKey asn1.BitString
	}
	rest, err := asn1.Unmarshal(der, &spki)
	if err != nil || len(rest) != 0 || !spki.Algorithm.OID.Equal(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 18}) || len(spki.Algorithm.Parameters.FullBytes) != 0 || spki.PublicKey.BitLength != 1952*8 {
		return nil, "", fmt.Errorf("invalid ML-DSA-65 SubjectPublicKeyInfo")
	}
	return map[string]string{"alg": "ML-DSA-65", "kty": "AKP", "pub": base64URL(spki.PublicKey.Bytes)}, "ML-DSA-65", nil
}

// OperatorJWKThumbprint computes the RFC 7638/9964 public key binding.
func OperatorJWKThumbprint(ctx context.Context, key platform.Signer) (string, error) {
	jwk, _, err := OperatorPublicJWK(ctx, key)
	if err != nil {
		return "", err
	}
	canonical := fmt.Sprintf(`{"alg":"%s","kty":"%s","pub":"%s"}`, jwk["alg"], jwk["kty"], jwk["pub"])
	sum := sha256.Sum256([]byte(canonical))
	return base64URL(sum[:]), nil
}

// SignJWS encodes a compact JWS and asks the host to sign exact input bytes.
func SignJWS(ctx context.Context, key platform.Signer, header, payload map[string]any) (string, error) {
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("marshaling JWS header: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshaling JWS payload: %w", err)
	}
	input := base64URL(headerJSON) + "." + base64URL(payloadJSON)
	signature, err := key.Sign(ctx, []byte(input))
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	return input + "." + base64URL(signature), nil
}

// DPoPProof constructs a token-endpoint proof, with a nonce when required.
func (c *Client) DPoPProof(ctx context.Context, key platform.Signer, method, target, nonce string) (string, error) {
	return c.dpopProof(ctx, key, method, target, nonce, "")
}

// DPoPAccessProof additionally binds the proof to exact access-token bytes.
func (c *Client) DPoPAccessProof(ctx context.Context, key platform.Signer, method, target, accessToken string) (string, error) {
	if accessToken == "" {
		return "", fmt.Errorf("access token is empty")
	}
	return c.dpopProof(ctx, key, method, target, "", accessToken)
}

func (c *Client) dpopProof(ctx context.Context, key platform.Signer, method, target, nonce, accessToken string) (string, error) {
	jwk, alg, err := OperatorPublicJWK(ctx, key)
	if err != nil {
		return "", err
	}
	jti, err := c.RandomURLSafe(ctx, 16)
	if err != nil {
		return "", fmt.Errorf("generating DPoP jti: %w", err)
	}
	payload := map[string]any{"jti": jti, "htm": method, "htu": target, "iat": time.Now().Unix()}
	if nonce != "" {
		payload["nonce"] = nonce
	}
	if accessToken != "" {
		sum := sha256.Sum256([]byte(accessToken))
		payload["ath"] = base64URL(sum[:])
	}
	return SignJWS(ctx, key, map[string]any{"typ": "dpop+jwt", "alg": alg, "jwk": jwk}, payload)
}

// CanonicalHTU removes query and fragment, as required by RFC 9449.
func CanonicalHTU(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parsing endpoint URL %q: %w", raw, err)
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Client) randomBytes(ctx context.Context, size int) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if c.Entropy == nil {
		return nil, fmt.Errorf("host entropy source is required")
	}
	raw, err := c.Entropy.RandomBytes(ctx, size)
	if err != nil {
		return nil, err
	}
	if len(raw) != size {
		return nil, fmt.Errorf("host entropy source returned the wrong number of bytes")
	}
	return raw, nil
}
