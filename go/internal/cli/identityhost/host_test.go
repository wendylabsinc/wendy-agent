package identityhost

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

func TestLiteIdentityKey(t *testing.T) {
	privatePEM, err := certs.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	key, err := IdentityKey(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := key.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("host-owned Lite key")
	signature, err := key.Sign(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(message)
	if len(signature) != 64 || !ecdsa.Verify(public.(*ecdsa.PublicKey), digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("invalid ES256 signature")
	}
	csrPEM, err := key.CertificateRequest(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		t.Fatal("missing CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if string(csr.RawSubjectPublicKeyInfo) != string(publicDER) {
		t.Fatal("CSR changed the host key")
	}
	principal, _ := url.Parse("spiffe://wendy.sh/tenant/11111111-1111-1111-1111-111111111111/operator/operator")
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), URIs: []*url.URL{principal}}
	signer, err := certs.ParseSigningPrivateKeyPEM([]byte(privatePEM))
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, signer)
	if err != nil {
		t.Fatal(err)
	}
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	opaque := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("opaque intermediate")})
	chain = append(chain, opaque...)
	identity, err := key.CheckCertificate(context.Background(), chain)
	if err != nil {
		t.Fatal(err)
	}
	if identity.PrincipalURI != principal.String() || identity.ChainPEM != string(opaque) {
		t.Fatalf("lost certificate identity or opaque chain: %+v", identity)
	}
	otherPEM, err := certs.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	other, err := IdentityKey(otherPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.CheckCertificate(context.Background(), chain); err == nil || !strings.Contains(err.Error(), "different key") {
		t.Fatalf("accepted another key: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := key.Sign(ctx, message); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled signing: %v", err)
	}
	if _, err := key.CertificateRequest(ctx, "operator"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled CSR: %v", err)
	}
	if _, err := key.CheckCertificate(ctx, chain); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled certificate check: %v", err)
	}
}

func TestIdentityKeyRejectsOtherECCurves(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := IdentityKey(string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))); err == nil {
		t.Fatal("accepted P-384 for ES256")
	}
}
