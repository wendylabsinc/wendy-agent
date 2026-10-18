// Package localkey is the CLI's local-operator key (WDY-3882): one ML-DSA-65
// key pair per CLI config directory, generated on first use and never rotated.
// `wendy os install` seeds its public key as a device's first owner key, and the
// CLI proves itself to such a device with a certificate it self-issues over this
// key — no cloud login, CA or network involved.
//
// It is deliberately not the per-session operator key: that one is replaced by
// every login and every certificate renewal, which would lock the owner out of
// every device seeded with it. Losing this key means another owner removes and
// re-adds you, or the device is reinstalled.
package localkey

import (
	"crypto"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// FileName is the key's file in the CLI config directory.
const FileName = "local-operator-key.pem"

// Path is where the key lives.
func Path() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// LoadOrCreate returns the key at path, generating it when absent.
func LoadOrCreate(path string) (crypto.Signer, error) {
	key, err := load(path)
	if !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	keyPEM, err := certs.GenerateMLDSAKeyPair()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	// Write a temp file and hard-link it into place: the link fails if another
	// CLI won the race, so a key some device may already trust is never
	// overwritten.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".local-operator-key-*")
	if err != nil {
		return nil, fmt.Errorf("creating local operator key: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck
	if _, err := tmp.WriteString(keyPEM); err != nil {
		tmp.Close() //nolint:errcheck
		return nil, fmt.Errorf("writing local operator key: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return nil, fmt.Errorf("writing local operator key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("writing local operator key: %w", err)
	}
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("storing local operator key: %w", err)
	}
	return load(path)
}

func load(path string) (crypto.Signer, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	// Like ssh: a private key others can read is not this operator's alone.
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is accessible by group or others (mode %#o); run chmod 600 on it", path, fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := certs.ParseSigningPrivateKeyPEM(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

// PublicKeyDER is the SubjectPublicKeyInfo DER of key — the form a device's
// trusted-keys file stores and matches.
func PublicKeyDER(key crypto.Signer) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(key.Public())
}
