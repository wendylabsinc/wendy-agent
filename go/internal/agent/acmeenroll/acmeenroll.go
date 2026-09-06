// Package acmeenroll obtains a device certificate from pki-core's ACME
// frontend using an External Account Binding (EAB) staged on the device.
//
// pki-core's ACME frontend differs from a public CA in ways that are invisible
// until a request fails with a generic 4xx, so they are called out where they
// bite:
//
//   - the EAB HMAC key is hex, not base64url (see Config.EABHMACKey);
//   - newOrder accepts only the "permanent-identifier" identifier type, and its
//     value must equal the DeviceID bound to the EAB row;
//   - finalize honours the CSR public key only. The CN and the URI SANs are
//     replaced server-side with spiffe://wendy.sh/tenant/<tenant>/device/<id>,
//     so this client asserts no identity of its own.
//
// The ACME account key is persisted because an EAB is single-use: re-registering
// the same account key is idempotent, but a new account key would need a fresh
// EAB that the device has no way to obtain.
package acmeenroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/acme"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// permanentIdentifier is the only identifier type pki-core's newOrder accepts
// (RFC 4043 / draft-acme-device-attest). "dns" and "ip" are rejected with 400.
const permanentIdentifier = "permanent-identifier"

// Config is the enrollment material staged on the device. The JSON tags are the
// on-disk shape written at provisioning time.
type Config struct {
	// DirectoryURL is the tenant's ACME directory,
	// https://<host>/<tenant-uuid>/acme/directory. The tenant is a canonical
	// lower-case UUID and rides inside this URL, so there is no tenant field.
	DirectoryURL string `json:"directoryURL"`
	// DeviceID is stamped into the issued SPIFFE identity server-side. It must
	// equal the DeviceID bound to the EAB row or newOrder returns 403.
	DeviceID string `json:"deviceID"`
	// EABKeyID is the opaque EAB key row id used as the JWS "kid".
	EABKeyID string `json:"eabKeyID"`
	// EABHMACKey is HEX-encoded, not the base64url every other ACME CA hands
	// out. Feeding the raw string to the MAC produces a well-formed request
	// that fails with an unhelpful 401.
	EABHMACKey string `json:"eabHMACKey"`
}

func (c Config) validate() error {
	var missing []string
	for _, f := range []struct {
		name, value string
	}{
		{"directoryURL", c.DirectoryURL},
		{"deviceID", c.DeviceID},
		{"eabKeyID", c.EABKeyID},
		{"eabHMACKey", c.EABHMACKey},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("acme enrollment config is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// Enroll registers the device's ACME account against the staged EAB, orders a
// certificate for its DeviceID and returns the issued leaf and the
// intermediates below it, both PEM-encoded. chainPEM is empty when the server
// returns a leaf alone.
//
// accountKeyPath is the persisted ACME account key; it is created on first call
// and reused afterwards. deviceKeyPEM is the device's own long-lived key, whose
// public half ends up in the issued certificate.
//
// TODO(WDY-2899): the tunnel broker dial deliberately presents leaf-only
// (internal/agent/services/tunnel_broker_client.go) because Go's TLS stack
// cannot parse pki-core's ML-DSA chain certificates. Whether the chain returned
// here can be presented there is an open ruling; this package only produces it.
func Enroll(ctx context.Context, cfg Config, accountKeyPath string, deviceKeyPEM []byte) (certPEM, chainPEM string, err error) {
	if err := cfg.validate(); err != nil {
		return "", "", err
	}

	hmacKey, err := hex.DecodeString(cfg.EABHMACKey)
	if err != nil {
		return "", "", fmt.Errorf("decoding EAB HMAC key (pki-core encodes it as hex): %w", err)
	}

	// The account key is created before anything is spent so a write failure
	// cannot burn the single-use EAB.
	accountKey, err := loadOrCreateAccountKey(accountKeyPath)
	if err != nil {
		return "", "", err
	}

	client := &acme.Client{Key: accountKey, DirectoryURL: cfg.DirectoryURL}
	account := &acme.Account{
		ExternalAccountBinding: &acme.ExternalAccountBinding{KID: cfg.EABKeyID, Key: hmacKey},
	}
	if _, err := client.Register(ctx, account, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return "", "", fmt.Errorf("registering ACME account: %w", err)
	}

	order, err := client.AuthorizeOrder(ctx, []acme.AuthzID{{Type: permanentIdentifier, Value: cfg.DeviceID}})
	if err != nil {
		return "", "", fmt.Errorf("creating ACME order for device %q: %w", cfg.DeviceID, err)
	}
	if order.Status != acme.StatusReady {
		return "", "", fmt.Errorf("ACME order is %q, want %q: this device's profile requires an attestation challenge, which is not implemented", order.Status, acme.StatusReady)
	}

	csrPEM, err := certs.GenerateCSR(deviceKeyPEM, cfg.DeviceID, nil, x509.ExtKeyUsageClientAuth)
	if err != nil {
		return "", "", fmt.Errorf("building device CSR: %w", err)
	}
	csrBlock, _ := pem.Decode([]byte(csrPEM))
	if csrBlock == nil {
		return "", "", errors.New("device CSR is not valid PEM")
	}

	ders, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csrBlock.Bytes, true)
	if err != nil {
		return "", "", fmt.Errorf("finalizing ACME order: %w", err)
	}
	if len(ders) == 0 {
		return "", "", errors.New("ACME server returned no certificate")
	}
	return encodeCerts(ders[:1]), encodeCerts(ders[1:]), nil
}

func encodeCerts(ders [][]byte) string {
	var b strings.Builder
	for _, der := range ders {
		b.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	return b.String()
}

// loadOrCreateAccountKey returns the persisted ACME account key, creating and
// storing a P-256 key (ES256, which pki-core's outer-JWS allow-list accepts) on
// first use.
func loadOrCreateAccountKey(path string) (*ecdsa.PrivateKey, error) {
	switch data, err := os.ReadFile(path); {
	case err == nil:
		key, err := parseECPrivateKeyPEM(data)
		if err != nil {
			return nil, fmt.Errorf("reading ACME account key %s: %w", path, err)
		}
		return key, nil
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("reading ACME account key %s: %w", path, err)
	}

	keyPEM, err := certs.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generating ACME account key: %w", err)
	}
	key, err := parseECPrivateKeyPEM([]byte(keyPEM))
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(path, []byte(keyPEM), 0o600); err != nil {
		return nil, fmt.Errorf("storing ACME account key %s: %w", path, err)
	}
	return key, nil
}

func parseECPrivateKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("ACME account key is not valid PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing ACME account key: %w", err)
	}
	return key, nil
}

// writeAtomic replaces path in one rename so a power loss cannot leave a
// half-written account key behind — the one file the device cannot regenerate
// on its own, since a new key would need a fresh single-use EAB.
//
// ponytail: no directory fsync, so a crash within the writeback window can
// still lose the whole file. That degrades to "no key", which is recoverable by
// re-staging, unlike a truncated one.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".acme-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename below succeeds
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
