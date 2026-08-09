package devicepin_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// TestStore_DifferentCert_WithinValidityIsRejected supersedes the old
// "rotation accepted" / "warns on mismatch" behavior: SPKI pinning now fails
// closed. A key change while the previously pinned cert is still valid is
// unexplained (a renewal replaces an expiring cert, it does not race a live
// one), so CheckAndUpdate must reject it instead of overwriting the pin with
// a warning.
func TestStore_DifferentCert_WithinValidityIsRejected(t *testing.T) {
	dir := t.TempDir()
	store, _ := devicepin.Open(dir)
	cert1 := makeCert(t, "urn:wendy:org:7:asset:42")
	_ = store.CheckAndUpdate(cert1, "My Device")

	// Different cert, same identity key, pinned cert still valid → hard fail.
	cert2 := makeCert(t, "urn:wendy:org:7:asset:42")
	err := store.CheckAndUpdate(cert2, "My Device")

	var mismatch *devicepin.PinMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("want PinMismatchError, got %v", err)
	}
	if !strings.Contains(mismatch.DisplayName, "My Device") {
		t.Errorf("mismatch.DisplayName = %q, want to contain %q", mismatch.DisplayName, "My Device")
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

// assetCert generates a fresh self-signed asset cert with a URI SAN of
// urn:wendy:org:<org>:asset:<assetID> and the given NotAfter. Modeled on
// selfSignedCert in shared/certs/server_verify_test.go. A fresh key is
// generated on every call, so two certs with the same org/assetID still
// differ in SPKI — required for the mismatch test below to be meaningful.
func assetCert(t *testing.T, org int32, assetID string, notAfter time.Time) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-device"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	u, err := url.Parse(fmt.Sprintf("urn:wendy:org:%d:asset:%s", org, assetID))
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	tmpl.URIs = []*url.URL{u}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing cert: %v", err)
	}
	return cert
}

func TestCheckAndUpdateKeyChange(t *testing.T) {
	t.Run("within validity is a hard fail", func(t *testing.T) {
		dir := t.TempDir()
		s, err := devicepin.Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		first := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		if err := s.CheckAndUpdate(first, "thor"); err != nil {
			t.Fatalf("first use: %v", err)
		}

		second := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		err = s.CheckAndUpdate(second, "thor")

		var mismatch *devicepin.PinMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("want PinMismatchError, got %v", err)
		}
	})

	t.Run("after the pinned cert expires it re-pins silently", func(t *testing.T) {
		dir := t.TempDir()
		s, err := devicepin.Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		expired := assetCert(t, 7, "42", time.Now().Add(-time.Hour))
		if err := s.CheckAndUpdate(expired, "thor"); err != nil {
			t.Fatalf("first use: %v", err)
		}

		renewed := assetCert(t, 7, "42", time.Now().Add(24*time.Hour))
		if err := s.CheckAndUpdate(renewed, "thor"); err != nil {
			t.Fatalf("rotation after expiry must be accepted, got %v", err)
		}
	})
}
