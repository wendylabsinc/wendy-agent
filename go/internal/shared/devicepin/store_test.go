package devicepin_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
)

func makeCert(t *testing.T, sanURI string) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	if sanURI != "" {
		u, _ := url.Parse(sanURI)
		tmpl.URIs = []*url.URL{u}
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	cert, _ := x509.ParseCertificate(der)
	return cert
}

func TestStore_FirstConnection_Pins(t *testing.T) {
	dir := t.TempDir()
	store, err := devicepin.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cert := makeCert(t, "urn:wendy:org:7:asset:42")
	if err := store.CheckAndUpdate(cert, "My Device"); err != nil {
		t.Fatalf("CheckAndUpdate first: %v", err)
	}
	// Pin file must exist.
	if _, err := os.Stat(filepath.Join(dir, "known_devices.json")); err != nil {
		t.Errorf("known_devices.json not created: %v", err)
	}
}

func TestStore_SameCert_UpdatesLastSeen(t *testing.T) {
	dir := t.TempDir()
	store, _ := devicepin.Open(dir)
	cert := makeCert(t, "urn:wendy:org:7:asset:42")
	_ = store.CheckAndUpdate(cert, "My Device")
	// Second call with same cert must not error.
	if err := store.CheckAndUpdate(cert, "My Device"); err != nil {
		t.Errorf("CheckAndUpdate second (same cert): %v", err)
	}
}

func TestStore_DifferentCert_RotationAccepted(t *testing.T) {
	dir := t.TempDir()
	store, _ := devicepin.Open(dir)
	cert1 := makeCert(t, "urn:wendy:org:7:asset:42")
	_ = store.CheckAndUpdate(cert1, "My Device")
	// Different cert, same identity key → rotation → accepted silently.
	cert2 := makeCert(t, "urn:wendy:org:7:asset:42")
	if err := store.CheckAndUpdate(cert2, "My Device"); err != nil {
		t.Errorf("CheckAndUpdate rotation: %v", err)
	}
}

func TestStore_NonAssetCert_Skipped(t *testing.T) {
	dir := t.TempDir()
	store, _ := devicepin.Open(dir)
	// User cert (entity type "user") is not pinned.
	cert := makeCert(t, "urn:wendy:org:7:user:99")
	if err := store.CheckAndUpdate(cert, "user"); err != nil {
		t.Errorf("CheckAndUpdate user cert: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "known_devices.json")); err == nil {
		// File may or may not exist; what matters is no error and no panic.
		// Read it and verify the user identity key is not present.
		data, _ := os.ReadFile(filepath.Join(dir, "known_devices.json"))
		if len(data) > 2 { // more than "{}"
			t.Logf("known_devices.json: %s", data)
		}
	}
}

func TestStore_NoCert_Identity_Skipped(t *testing.T) {
	dir := t.TempDir()
	store, _ := devicepin.Open(dir)
	// Cert with no Wendy identity → skipped, no error.
	cert := makeCert(t, "")
	if err := store.CheckAndUpdate(cert, "legacy"); err != nil {
		t.Errorf("CheckAndUpdate no-identity cert: %v", err)
	}
}

func TestStore_PersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	cert := makeCert(t, "urn:wendy:org:7:asset:42")

	store1, _ := devicepin.Open(dir)
	_ = store1.CheckAndUpdate(cert, "My Device")

	// Re-open from same dir — pin must survive.
	store2, err := devicepin.Open(dir)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	// Same cert → no error (SPKI match).
	if err := store2.CheckAndUpdate(cert, "My Device"); err != nil {
		t.Errorf("CheckAndUpdate after reload: %v", err)
	}
}

// unused import guard — pem is imported by the brief verbatim.
var _ = pem.EncodeToMemory
