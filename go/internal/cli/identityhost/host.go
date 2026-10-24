// Package identityhost adapts CLI-owned keys and HTTP clients to WendyGo.
package identityhost

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/mldsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"

	wendy "github.com/wendylabsinc/wendy-go"
	"github.com/wendylabsinc/wendy-go/identity"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// HTTPDoer is a host HTTP client, including the browser's relay client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}
type doerTransport struct{ HTTPDoer }

func (t doerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t.Do(r) }

// Client retains the host's networking and TLS while WendyGo supplies adapters.
func Client(doer HTTPDoer) *identity.Client {
	var client http.Client
	if native, ok := doer.(*http.Client); ok {
		client = *native
	} else {
		client.Transport = doerTransport{doer}
	}
	// The concrete client address satisfies NewHTTPTransport's nonnil precondition.
	transport, _ := wendy.NewHTTPTransport(&client)
	return &identity.Client{HTTP: transport, Verifier: wendy.GoSignatureVerifier{}, Entropy: wendy.CryptoEntropy{}}
}

// OperatorKey wraps the CLI's existing ML-DSA key without exporting it to Core.
func OperatorKey(key crypto.Signer) (*wendy.MLDSA65Signer, error) {
	native, ok := key.(*mldsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("this session's operator key is %T, but Wendy now requires an ML-DSA-65 operator credential; re-run 'wendy auth login'", key.Public())
	}
	return wendy.NewMLDSA65Signer(native, nil)
}

// IdentityKey supplies the host key used for a primary or secondary Lite CSR.
func IdentityKey(privatePEM string) (wendy.IdentityKey, error) {
	key, err := certs.ParseSigningPrivateKeyPEM([]byte(privatePEM))
	if err != nil {
		return nil, err
	}
	if _, ok := key.(*mldsa.PrivateKey); ok {
		return OperatorKey(key)
	}
	if key, ok := key.(*ecdsa.PrivateKey); ok && key.Curve == elliptic.P256() {
		return &liteKey{key: key, privatePEM: privatePEM}, nil
	}
	return nil, fmt.Errorf("unsupported identity CSR key %T", key)
}

// Certificate restores only the host-owned key field omitted by portable Core.
func Certificate(c identity.Certificate, privatePEM string) config.CertificateInfo {
	return config.CertificateInfo{
		PemCertificate:      c.PemCertificate,
		PemCertificateChain: c.PemCertificateChain,
		PemPrivateKey:       privatePEM,
		PrincipalURI:        c.PrincipalURI,
		OrganizationID:      c.OrganizationID,
	}
}

type liteKey struct {
	key        *ecdsa.PrivateKey
	privatePEM string
}

func (*liteKey) Algorithm() wendy.SignatureAlgorithm                { return "ES256" }
func (*liteKey) CertificateChain(context.Context) ([][]byte, error) { return nil, nil }
func (k *liteKey) PublicKey(ctx context.Context) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return x509.MarshalPKIXPublicKey(k.key.Public())
}
func (k *liteKey) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, k.key, digest[:])
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signature, nil
}
func (k *liteKey) CertificateRequest(ctx context.Context, subject string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	csr, err := certs.GenerateCSR([]byte(k.privatePEM), subject, nil)
	return []byte(csr), err
}
func (k *liteKey) CheckCertificate(ctx context.Context, chain []byte) (wendy.CertificateIdentity, error) {
	if ctx.Err() != nil {
		return wendy.CertificateIdentity{}, ctx.Err()
	}
	leafPEM, chainPEM, leaf, err := SplitCertificateChainPEM(chain)
	if err != nil {
		return wendy.CertificateIdentity{}, fmt.Errorf("parsing pki-core certificate chain: %w", err)
	}
	want, err := x509.MarshalPKIXPublicKey(k.key.Public())
	if err != nil {
		return wendy.CertificateIdentity{}, fmt.Errorf("encoding generated operator public key: %w", err)
	}
	if !bytes.Equal(want, leaf.RawSubjectPublicKeyInfo) {
		return wendy.CertificateIdentity{}, fmt.Errorf("pki-core returned a certificate for a different key")
	}
	principal, ok := certs.TenantPrincipalFromCert(leaf)
	if !ok {
		return wendy.CertificateIdentity{}, fmt.Errorf("pki-core certificate must contain exactly one tenant SPIFFE principal")
	}
	return wendy.CertificateIdentity{CertificatePEM: string(leafPEM), ChainPEM: string(chainPEM), PrincipalURI: principal}, nil
}

// SplitCertificateChainPEM parses the leaf while preserving opaque chain certificates.
func SplitCertificateChainPEM(chain []byte) (string, string, *x509.Certificate, error) {
	rest := chain
	var encoded [][]byte
	var leaf *x509.Certificate
	for len(bytes.TrimSpace(rest)) > 0 {
		block, remaining := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", "", nil, fmt.Errorf("response contains non-certificate PEM data")
		}
		if leaf == nil {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return "", "", nil, err
			}
			leaf = cert
		}
		encoded = append(encoded, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}))
		rest = remaining
	}
	if leaf == nil {
		return "", "", nil, fmt.Errorf("response contains no certificate")
	}
	return string(encoded[0]), string(bytes.Join(encoded[1:], nil)), leaf, nil
}
