package commands

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testEnrollmentToken(t *testing.T, payload string) string {
	t.Helper()
	return "header." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func TestEnrollmentTokenCommonName_UserEnrollment(t *testing.T) {
	token := testEnrollmentToken(t, `{"type":"user_enrollment","org_id":1,"user_id":"user-123"}`)

	got, err := enrollmentTokenCommonName(token)
	if err != nil {
		t.Fatalf("enrollmentTokenCommonName() error = %v", err)
	}
	if got != "wendy/user/user-123" {
		t.Fatalf("enrollmentTokenCommonName() = %q, want %q", got, "wendy/user/user-123")
	}
}

func TestEnrollmentTokenCommonName_AssetEnrollment(t *testing.T) {
	token := testEnrollmentToken(t, `{"type":"asset_enrollment","org_id":7,"asset_id":42}`)

	got, err := enrollmentTokenCommonName(token)
	if err != nil {
		t.Fatalf("enrollmentTokenCommonName() error = %v", err)
	}
	if got != "wendy/7/42" {
		t.Fatalf("enrollmentTokenCommonName() = %q, want %q", got, "wendy/7/42")
	}
}

func TestEnrollmentTokenCommonName_InvalidToken(t *testing.T) {
	if _, err := enrollmentTokenCommonName("not-a-jwt"); err == nil {
		t.Fatal("expected invalid token error")
	}
}

func selfSignedCertPEM(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCertCommonName(t *testing.T) {
	const wantCN = "wendy/user/3VBQnKRlcFMOFjnjyw8ca7Rk6jR2"
	certPEM := selfSignedCertPEM(t, wantCN)

	got, err := certCommonName(certPEM)
	if err != nil {
		t.Fatalf("certCommonName() error = %v", err)
	}
	if got != wantCN {
		t.Fatalf("certCommonName() = %q, want %q", got, wantCN)
	}
}

func TestCertCommonName_InvalidPEM(t *testing.T) {
	if _, err := certCommonName("not-a-pem"); err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}
