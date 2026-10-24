package identity

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"fmt"
	mldsa "github.com/cloudflare/circl/sign/mldsa/mldsa65"
	"github.com/wendylabsinc/WendyOS/go/wendy-core/platform"
	"math/big"
	"net/http"
)

type oidcProviderMetadata = ProviderMetadata

const oidcScopes = Scopes

var audienceContains = AudienceContains
var confirmationThumbprint = ConfirmationThumbprint
var decodeJWTClaims = DecodeJWTClaims
var buildAuthorizeURL = BuildAuthorizeURL
var effectiveLoginIssuer = EffectiveLoginIssuer
var canonicalAuthorizationBase = CanonicalAuthorizationBase
var sameIssuerOrigin = SameIssuerOrigin

func newPKCEVerifier() (string, string, error) {
	return (&Client{Entropy: testEntropy{}}).NewPKCEVerifier(context.Background())
}

var canonicalHTU = CanonicalHTU

func operatorPublicJWK(key crypto.Signer) (map[string]string, string, error) {
	return OperatorPublicJWK(context.Background(), testSigner{key})
}
func operatorJWKThumbprint(key crypto.Signer) (string, error) {
	return OperatorJWKThumbprint(context.Background(), testSigner{key})
}
func newDPoPProof(key crypto.Signer, method, target, nonce string) (string, error) {
	return (&Client{Entropy: testEntropy{}}).DPoPProof(context.Background(), testSigner{key}, method, target, nonce)
}
func discoverOIDCIssuer(ctx context.Context, authority, email string) (string, error) {
	return (&Client{Entropy: testEntropy{}, HTTP: testHTTP(http.DefaultClient.Do)}).DiscoverIssuer(ctx, authority, email)
}
func refreshOIDCToken(ctx context.Context, key crypto.Signer, meta *ProviderMetadata, client, refresh, resource string) (*TokenResponse, error) {
	return (&Client{Entropy: testEntropy{}, HTTP: testHTTP(http.DefaultClient.Do)}).RefreshToken(ctx, testSigner{key}, meta, client, refresh, resource)
}

type testSigner struct{ key crypto.Signer }

func (s testSigner) Algorithm() platform.SignatureAlgorithm { return platform.MLDSA65 }
func (s testSigner) PublicKey(ctx context.Context) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	pub := s.key.Public().(*mldsa.PublicKey).Bytes()
	return asn1.Marshal(struct {
		Algorithm struct{ OID asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}{Algorithm: struct{ OID asn1.ObjectIdentifier }{asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 18}}, PublicKey: asn1.BitString{Bytes: pub, BitLength: len(pub) * 8}})
}
func (s testSigner) CertificateChain(context.Context) ([][]byte, error) { return nil, nil }
func (s testSigner) Sign(ctx context.Context, b []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return s.key.Sign(rand.Reader, b, crypto.Hash(0))
}

type testSignatureVerifier struct{}

func (testSignatureVerifier) VerifySignature(ctx context.Context, key map[string]string, message, signature []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch key["alg"] {
	case "ES256":
		if verifyES256JWK(key, message, signature) {
			return nil
		}
	case "ML-DSA-65":
		if verifyMLDSA65JWK(key, message, signature) {
			return nil
		}
	}
	return fmt.Errorf("invalid signature")
}
func verifyES256JWK(key JWK, message, signature []byte) bool {
	if key["kty"] != "EC" || key["crv"] != "P-256" || len(signature) != 64 {
		return false
	}
	xBytes, errX := base64.RawURLEncoding.DecodeString(key["x"])
	yBytes, errY := base64.RawURLEncoding.DecodeString(key["y"])
	if errX != nil || errY != nil {
		return false
	}
	x, y := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes)
	if !elliptic.P256().IsOnCurve(x, y) {
		return false
	}
	digest := sha256.Sum256(message)
	return ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:],
		new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:]))
}

func verifyMLDSA65JWK(key JWK, message, signature []byte) bool {
	if key["kty"] != "AKP" {
		return false
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(key["pub"])
	if err != nil {
		return false
	}
	var publicKey mldsa.PublicKey
	err = publicKey.UnmarshalBinary(publicBytes)
	return err == nil && mldsa.Verify(&publicKey, message, nil, signature)
}

type testEntropy struct{}

func (testEntropy) RandomBytes(ctx context.Context, size int) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	raw := make([]byte, size)
	_, err := rand.Read(raw)
	return raw, err
}
