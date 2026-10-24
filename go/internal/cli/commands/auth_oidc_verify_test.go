package commands

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/identityhost"
)

func testES256Token(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func testES256JWK(key *ecdsa.PrivateKey, kid string) oidcJWK {
	return oidcJWK{
		"kid": kid, "alg": "ES256", "kty": "EC", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(key.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(key.Y.FillBytes(make([]byte, 32))),
	}
}

func TestOIDCTokenVerifierValidatesSignatureAndClaims(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://auth.example/realms/acme"
	verifier := testOIDCTokenVerifier(t, issuer, []oidcJWK{testES256JWK(key, "key-1")})
	claims := map[string]any{
		"iss": issuer, "aud": "wendy-cli", "sub": "user-1", "nonce": "nonce-1",
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	accessClaims := map[string]any{
		"iss": issuer, "aud": "https://pki.example/identity", "sub": "user-1",
		"exp": time.Now().Add(time.Hour).Unix(), "cnf": map[string]string{"jkt": "thumbprint"},
	}
	access := testES256Token(t, key, "key-1", accessClaims)
	digest := sha256.Sum256([]byte(access))
	claims["at_hash"] = base64.RawURLEncoding.EncodeToString(digest[:16])
	idToken := testES256Token(t, key, "key-1", claims)

	subject, err := verifyIDToken(verifier, &oidcTokenResponse{IDToken: idToken, AccessToken: access}, "wendy-cli", "nonce-1")
	if err != nil || subject != "user-1" {
		t.Fatalf("verifyIDToken = %q, %v", subject, err)
	}
	if _, err := verifyAccessToken(verifier, access, "https://pki.example/identity", "thumbprint"); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCTokenVerifierAcceptsMLDSA65(t *testing.T) {
	key, err := mldsa.GenerateKey(mldsa.MLDSA65())
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://auth.example/realms/acme"
	header, _ := json.Marshal(map[string]string{"alg": "ML-DSA-65", "kid": "pq"})
	payload, _ := json.Marshal(map[string]any{
		"iss": issuer, "aud": "wendy-cli", "sub": "user-1", "exp": time.Now().Add(time.Hour).Unix(),
	})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	signature, err := key.Sign(rand.Reader, []byte(input), crypto.Hash(0))
	if err != nil {
		t.Fatal(err)
	}
	token := input + "." + base64.RawURLEncoding.EncodeToString(signature)
	verifier := testOIDCTokenVerifier(t, issuer, []oidcJWK{{
		"kid": "pq", "alg": "ML-DSA-65", "kty": "AKP",
		"pub": base64.RawURLEncoding.EncodeToString(key.Public().(*mldsa.PublicKey).Bytes()),
	}})
	if _, err := verifier.Verify(context.Background(), token, "wendy-cli"); err != nil {
		t.Fatal(err)
	}
}

func TestOIDCTokenVerifierRejectsTamperingAndNonceMismatch(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const issuer = "https://auth.example/realms/acme"
	verifier := testOIDCTokenVerifier(t, issuer, []oidcJWK{testES256JWK(key, "key-1")})
	token := testES256Token(t, key, "key-1", map[string]any{
		"iss": issuer, "aud": "wendy-cli", "sub": "user-1", "nonce": "wrong",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	if _, err := verifyIDToken(verifier, &oidcTokenResponse{IDToken: token, AccessToken: "access"}, "wendy-cli", "expected"); err == nil {
		t.Fatal("nonce mismatch was accepted")
	}
	parts := []byte(token)
	parts[len(parts)-1] ^= 1
	if _, err := verifier.Verify(context.Background(), string(parts), "wendy-cli"); err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

func TestVerifyES256JWKRejectsInvalidPoint(t *testing.T) {
	key := oidcJWK{"kty": "EC", "crv": "P-256", "x": base64.RawURLEncoding.EncodeToString(big.NewInt(1).Bytes()), "y": base64.RawURLEncoding.EncodeToString(big.NewInt(1).Bytes())}
	if verifyES256JWK(key, []byte("message"), make([]byte, 64)) {
		t.Fatal("invalid curve point was accepted")
	}
}

// The SDK owns the verifier's private state. Load test keys through its public
// host interface; all existing signature, claim and nonce assertions stay intact.
func testOIDCTokenVerifier(t *testing.T, issuer string, keys []oidcJWK) *oidcTokenVerifier {
	t.Helper()
	document, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		t.Fatal(err)
	}
	client := identityhost.Client(oidcVerifierHTTP(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(document))}, nil
	}))
	verifier, err := client.LoadTokenVerifier(context.Background(), &oidcProviderMetadata{Issuer: issuer, JWKSURI: "https://issuer.invalid/jwks"})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

type oidcVerifierHTTP func(*http.Request) (*http.Response, error)

func (f oidcVerifierHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }
