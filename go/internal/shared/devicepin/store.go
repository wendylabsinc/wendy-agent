// Package devicepin persists and verifies SPKI fingerprints for known Wendy
// devices, providing TOFU (trust-on-first-use) protection against MITM.
package devicepin

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

const pinFileName = "known_devices.json"

// PinnedDevice records the last-seen SPKI fingerprint for a device identity.
type PinnedDevice struct {
	SPKIFingerprint string `json:"spkiFingerprint"` // "sha256:<hex>"
	DisplayName     string `json:"displayName"`
	LastSeen        string `json:"lastSeen"`           // RFC3339
	NotAfter        string `json:"notAfter,omitempty"` // RFC3339; pinned leaf's expiry
}

// PinMismatchError reports that a device identity presented a different public
// key than the one pinned for it, while the pinned certificate was still valid.
// A rotation that happens after the pinned cert expires is not an error — it is
// renewal — so this only fires inside the pinned cert's validity window, where
// a new key is unexplained.
type PinMismatchError struct {
	Key         string
	DisplayName string
	Want        string
	Got         string
}

func (e *PinMismatchError) Error() string {
	return fmt.Sprintf("device %q (%s) presented a different certificate key than pinned (pinned %s, now %s)",
		e.DisplayName, e.Key, e.Want, e.Got)
}

// Store is a file-backed map from device identity key to PinnedDevice.
// It is not safe for concurrent use across multiple processes.
type Store struct {
	path    string
	devices map[string]PinnedDevice
}

// Open loads the pin store from dir/known_devices.json, creating it if absent.
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, pinFileName)
	s := &Store{path: path, devices: make(map[string]PinnedDevice)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("reading pin store: %w", err)
	}
	if err := json.Unmarshal(data, &s.devices); err != nil {
		// Corrupt file: start fresh rather than block all connections.
		s.devices = make(map[string]PinnedDevice)
	}
	return s, nil
}

// CheckAndUpdate checks the stored pin for the device identified by leaf's
// Wendy identity, creating or updating it as needed.
//
//   - Not an asset cert: skip (user certs and certs with no identity are not pinned)
//   - Not previously pinned: store pin, return nil
//   - Pinned, SPKI match: update LastSeen, return nil
//   - Pinned, SPKI differs, pinned cert still valid: hard fail with PinMismatchError
//   - Pinned, SPKI differs, pinned cert expired (or predates NotAfter tracking):
//     rotation by definition — update pin, return nil
func (s *Store) CheckAndUpdate(leaf *x509.Certificate, displayName string) error {
	identity, ok, err := certs.IdentityFromCert(leaf)
	if err != nil || !ok || identity.EntityType != "asset" {
		return nil
	}

	key := identity.IdentityKey()
	fingerprint := spkiFingerprint(leaf)

	if existing, pinned := s.devices[key]; pinned && existing.SPKIFingerprint != fingerprint {
		// A key change while the pinned cert is still valid is unexplained: a
		// renewal replaces an expiring cert, it does not race a live one.
		if existing.NotAfter != "" {
			if exp, parseErr := time.Parse(time.RFC3339, existing.NotAfter); parseErr == nil && time.Now().Before(exp) {
				return &PinMismatchError{Key: key, DisplayName: displayName, Want: existing.SPKIFingerprint, Got: fingerprint}
			}
		}
	}

	s.devices[key] = PinnedDevice{
		SPKIFingerprint: fingerprint,
		DisplayName:     displayName,
		LastSeen:        time.Now().UTC().Format(time.RFC3339),
		NotAfter:        leaf.NotAfter.UTC().Format(time.RFC3339),
	}
	return s.flush()
}

func (s *Store) flush() error {
	data, err := json.MarshalIndent(s.devices, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling pin store: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("writing pin store: %w", err)
	}
	return nil
}

func spkiFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256:" + hex.EncodeToString(sum[:])
}
