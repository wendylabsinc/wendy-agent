package certs_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// selfSignedCert creates a minimal self-signed ECDSA cert with an optional SAN URI.
func selfSignedCert(t *testing.T, cn string, sanURI string) (*x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	if sanURI != "" {
		u, uriErr := url.Parse(sanURI)
		if uriErr != nil {
			t.Fatalf("url.Parse(%q): %v", sanURI, uriErr)
		}
		tmpl.URIs = []*url.URL{u}
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating cert: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parsing cert: %v", err)
	}
	chainPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	return cert, chainPEM
}

func TestBuildServerVerifyConnection_OrgMismatch(t *testing.T) {
	// Self-signed cert for org 7, expected org 5 → OrgMismatchError
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 5,
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	err = verifyConn(cs)

	var mismatch *certs.OrgMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected OrgMismatchError, got %v", err)
	}
	if mismatch.Want != 5 || mismatch.Got != 7 {
		t.Errorf("OrgMismatchError = {%d, %d}, want {5, 7}", mismatch.Want, mismatch.Got)
	}
}

func TestBuildServerVerifyConnection_OrgMatch(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 7,
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestBuildServerVerifyConnection_ZeroOrgAcceptsAny(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 0, // accept any
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestBuildServerVerifyConnection_PinStoreCalledOnSuccess(t *testing.T) {
	serverCert, chainPEM := selfSignedCert(t, "device", "urn:wendy:org:7:asset:42")

	called := false
	pin := &fakePinChecker{onCheck: func(leaf *x509.Certificate, name string) error {
		called = true
		return nil
	}}

	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      string(chainPEM),
		ExpectedOrgID: 7,
		PinStore:      pin,
	})
	if err != nil {
		t.Fatalf("BuildServerVerifyConnection: %v", err)
	}

	cs := tls.ConnectionState{PeerCertificates: []*x509.Certificate{serverCert}}
	if err := verifyConn(cs); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if !called {
		t.Error("PinStore.CheckAndUpdate was not called")
	}
}

type fakePinChecker struct {
	onCheck func(*x509.Certificate, string) error
}

func (f *fakePinChecker) CheckAndUpdate(leaf *x509.Certificate, displayName string) error {
	return f.onCheck(leaf, displayName)
}
