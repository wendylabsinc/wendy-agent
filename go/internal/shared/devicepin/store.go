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
	LastSeen        string `json:"lastSeen"` // RFC3339
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
//   - Pinned, SPKI differs: warn to stderr (potential MITM or legitimate rotation),
//     update pin, return nil
func (s *Store) CheckAndUpdate(leaf *x509.Certificate, displayName string) error {
	identity, ok, err := certs.IdentityFromCert(leaf)
	if err != nil || !ok || identity.EntityType != "asset" {
		return nil
	}

	key := identity.IdentityKey()
	fingerprint := spkiFingerprint(leaf)

	if existing, pinned := s.devices[key]; pinned && existing.SPKIFingerprint != fingerprint {
		fmt.Fprintf(os.Stderr,
			"WARNING: Device %q (%s) presented a different certificate than previously seen (was: %s, now: %s); if this is unexpected, a MITM attack may be in progress.\n",
			displayName, key, existing.SPKIFingerprint, fingerprint)
	}

	s.devices[key] = PinnedDevice{
		SPKIFingerprint: fingerprint,
		DisplayName:     displayName,
		LastSeen:        time.Now().UTC().Format(time.RFC3339),
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
