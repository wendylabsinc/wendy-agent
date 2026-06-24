package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// testLeafCertificate generates a proper leaf (end-entity) certificate for testing.
func testLeafCertificate(t *testing.T, commonName string) (certPEM, keyPEM string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// testCACertificate generates a self-signed CA certificate for testing.
func testCACertificate(t *testing.T, commonName string) (certPEM, keyPEM string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}))

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

// TestBuildVerifyPeerCertificate_StandardPathHonorsNotBeforeFloor verifies that
// the NotBefore floor is applied to the standard (RSA/ECDSA) x509 verification
// path through VerifyOptions.CurrentTime — not only the ML-DSA path. A leaf
// whose NotBefore is in the future relative to the real clock is rejected
// without a floor but accepted once the floor advances the effective time to its
// NotBefore. Guards against a regression where the floor is dropped from the
// standard path and devices with stale clocks reject valid client certs again.
func TestBuildVerifyPeerCertificate_StandardPathHonorsNotBeforeFloor(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Floor Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing CA cert: %v", err)
	}

	// Leaf NotBefore is one hour in the future — "not yet valid" against the real clock.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating leaf key: %v", err)
	}
	notBefore := time.Now().Add(time.Hour)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "future-leaf"},
		NotBefore:    notBefore,
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating leaf cert: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)
	caCerts := []*x509.Certificate{caCert}
	rawCerts := [][]byte{leafDER}

	// Without a floor the real clock precedes the leaf's NotBefore → rejected.
	if err := buildVerifyPeerCertificate(caPool, caCerts, nil, time.Time{})(rawCerts, nil); err == nil {
		t.Fatal("expected rejection without floor (leaf not yet valid), got nil")
	}

	// With the floor at the leaf's NotBefore the effective time advances to it → accepted.
	if err := buildVerifyPeerCertificate(caPool, caCerts, nil, notBefore)(rawCerts, nil); err != nil {
		t.Errorf("expected acceptance with floor=%v, got: %v", notBefore, err)
	}
}

// TestBuildVerifyPeerCertificate_ClientCertIssuedAfterFloor covers the case
// where a device's RTC is stuck at epoch (pre-NTP), its notBeforeFloor is set
// to the device cert's NotBefore (e.g. June 17), but the connecting CLI user
// cert was issued after that floor (e.g. June 23). Without the fix, effectiveNow
// = floor (June 17) < client cert NotBefore (June 23), which causes a spurious
// "certificate not yet valid" rejection.
func TestBuildVerifyPeerCertificate_ClientCertIssuedAfterFloor(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Cloud Root CA"},
		NotBefore:             time.Now().Add(-30 * 24 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating CA cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parsing CA cert: %v", err)
	}

	// Simulate device provisioned 6 days ago → floor = 6 days ago.
	deviceProvisionedAt := time.Now().Add(-6 * 24 * time.Hour)
	notBeforeFloor := deviceProvisionedAt

	// Client cert issued today (after the floor). NotBefore = now (within the last minute).
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating leaf key: %v", err)
	}
	clientNotBefore := time.Now().Add(-time.Minute) // issued just now
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "wendy/user/testuser"},
		NotBefore:    clientNotBefore,
		NotAfter:     clientNotBefore.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("creating leaf cert: %v", err)
	}

	caPool := x509.NewCertPool()
	caPool.AddCert(caCert)
	caCerts := []*x509.Certificate{caCert}
	rawCerts := [][]byte{leafDER}

	// Simulate device clock stuck at epoch — real clock is far behind the floor.
	// (We can't override time.Now, but we can verify that with the floor at
	// notBeforeFloor and the real clock as-is the cert is accepted. The test
	// exercises the branch where realNow < notBeforeFloor is NOT true since we
	// can't freeze time.Now in unit tests, so we confirm the cert is accepted
	// when the floor is set to a time before the cert's NotBefore — the normal
	// post-fix path — and rejected when the floor is behind the NotBefore without
	// the fix, which the preceding TestBuildVerifyPeerCertificate_StandardPathHonorsNotBeforeFloor
	// already covers. What this test adds: the floor is EARLIER than the client
	// cert's NotBefore, confirming the cert is accepted regardless.)
	if err := buildVerifyPeerCertificate(caPool, caCerts, nil, notBeforeFloor)(rawCerts, nil); err != nil {
		t.Errorf("client cert issued after floor should be accepted, got: %v", err)
	}
}

func TestNewTLSConfigEmptyChainReturnsError(t *testing.T) {
	certPEM, keyPEM := testLeafCertificate(t, "leaf")

	_, err := NewTLSConfig(certPEM, "", keyPEM, nil, time.Time{})
	if err == nil {
		t.Fatal("NewTLSConfig() expected error for empty chainPEM, got nil")
	}
}

func TestNewTLSConfigServesOnlyLeafCertificate(t *testing.T) {
	leafPEM, keyPEM := testLeafCertificate(t, "leaf")
	chainPEM, _ := testCACertificate(t, "chain")

	tlsConfig, err := NewTLSConfig(leafPEM+"\n"+chainPEM, chainPEM, keyPEM, nil, time.Time{})
	if err != nil {
		t.Fatalf("NewTLSConfig() error = %v", err)
	}

	if got := len(tlsConfig.Certificates[0].Certificate); got != 1 {
		t.Fatalf("served certificate chain length = %d; want 1", got)
	}
}
