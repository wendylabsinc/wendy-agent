// Command registry-custodian-unwrap opens a registry custodian wrap with the
// Cloud KMS custodian key (KEM_XWING) as the signed-in human and prints the
// wrapped 32-byte key as hex.
//
//	registry-custodian-unwrap projects/P/locations/L/keyRings/R/cryptoKeys/K/cryptoKeyVersions/V < wrap.json
//
// wrap.json is the KeyWrap (wendycloud/v2/registry.proto) in proto JSON plus
// the "organizationId" it was wrapped for. KMS only decapsulates; the HPKE
// base-mode key schedule and AES-GCM open run here, because a remote X-Wing
// key cannot back a Go hpke.PrivateKey. Layout: service-protos
// conformance/registry/README.md ("key wrap").
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// wrap is the subset of the KeyWrap proto JSON the custodian open needs.
type wrap struct {
	OrganizationID string `json:"organizationId"`
	Scope          struct {
		DeploymentID string `json:"deploymentId"`
		AppReleaseID string `json:"appReleaseId"`
		KeyID        string `json:"keyId"`
	} `json:"scope"`
	RecipientKeyID string `json:"recipientKeyId"`
	Enc            []byte `json:"enc"`
	Ciphertext     []byte `json:"ciphertext"`
}

func main() {
	key, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry-custodian-unwrap:", err)
		os.Exit(1)
	}
	fmt.Println(hex.EncodeToString(key))
}

func run() ([]byte, error) {
	if len(os.Args) != 2 {
		return nil, errors.New("usage: registry-custodian-unwrap <kms-key-version-resource> < wrap.json")
	}
	v, err := parseVersion(os.Args[1])
	if err != nil {
		return nil, err
	}
	var w wrap
	if err := json.NewDecoder(os.Stdin).Decode(&w); err != nil {
		return nil, fmt.Errorf("read wrap: %w", err)
	}
	dir, err := os.MkdirTemp("", "custodian-unwrap-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	pubFile := filepath.Join(dir, "pub")
	if err := gcloud(v, "keys", "versions", "get-public-key", v.version, "--public-key-format=xwing-raw-bytes", "--output-file="+pubFile); err != nil {
		return nil, err
	}
	pub, err := os.ReadFile(pubFile)
	if err != nil {
		return nil, err
	}
	return unwrap(w, pub, func(enc []byte) ([]byte, error) {
		encFile, ssFile := filepath.Join(dir, "enc"), filepath.Join(dir, "ss")
		if err := os.WriteFile(encFile, enc, 0o600); err != nil {
			return nil, err
		}
		if err := gcloud(v, "decapsulate", "--version="+v.version, "--ciphertext-file="+encFile, "--shared-secret-file="+ssFile); err != nil {
			return nil, err
		}
		return os.ReadFile(ssFile)
	})
}

type keyVersion struct{ project, location, keyRing, key, version string }

func parseVersion(name string) (keyVersion, error) {
	p := strings.Split(name, "/")
	if len(p) != 10 || p[0] != "projects" || p[2] != "locations" || p[4] != "keyRings" || p[6] != "cryptoKeys" || p[8] != "cryptoKeyVersions" {
		return keyVersion{}, fmt.Errorf("%q is not projects/P/locations/L/keyRings/R/cryptoKeys/K/cryptoKeyVersions/V", name)
	}
	return keyVersion{p[1], p[3], p[5], p[7], p[9]}, nil
}

// gcloud runs `gcloud kms <args>` on the key; its stderr (auth prompts,
// permission errors) goes straight to ours.
func gcloud(v keyVersion, args ...string) error {
	args = append(append([]string{"kms"}, args...), "--project="+v.project, "--location="+v.location, "--keyring="+v.keyRing, "--key="+v.key)
	cmd := exec.Command("gcloud", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gcloud %s: %w", strings.Join(args[:2], " "), err)
	}
	return nil
}

// unwrap refuses a wrap made to another key than pub, decapsulates its enc
// with decap and opens it: HPKE base mode (RFC 9180), X-Wing / HKDF-SHA256 /
// AES-256-GCM, custodian info, empty AAD.
func unwrap(w wrap, pub []byte, decap func(enc []byte) ([]byte, error)) ([]byte, error) {
	if keyID(pub) != w.RecipientKeyID {
		return nil, fmt.Errorf("wrap recipient_key_id %s is not this KMS key version (%s)", w.RecipientKeyID, keyID(pub))
	}
	info, err := w.info()
	if err != nil {
		return nil, err
	}
	ss, err := decap(w.Enc)
	if err != nil {
		return nil, err
	}
	gcm, nonce, err := keySchedule(ss, info)
	if err != nil {
		return nil, err
	}
	key, err := gcm.Open(nil, nonce, w.Ciphertext, nil)
	if err != nil {
		return nil, errors.New("wrap does not authenticate")
	}
	if keyID(key) != w.Scope.KeyID {
		return nil, errors.New("unwrapped key does not match the wrap's key_id")
	}
	return key, nil
}

func keyID(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// info = "wendy-registry-wrap-v1" ‖ lp(org) ‖ u8 scope kind ‖ lp(scope id) ‖
// lp(key_id) ‖ u8 recipient_kind ‖ lp(recipient) ‖ lp(recipient_key_id), with
// recipient_kind 3 (CUSTODIAN) and an empty recipient.
func (w wrap) info() ([]byte, error) {
	scope, scopeID := byte(1), w.Scope.DeploymentID
	if scopeID == "" {
		scope, scopeID = 2, w.Scope.AppReleaseID
	}
	for _, f := range []string{w.OrganizationID, scopeID, w.Scope.KeyID, w.RecipientKeyID} {
		if len(f) > math.MaxUint16 {
			return nil, errors.New("wrap binding field too long")
		}
	}
	b := lp([]byte("wendy-registry-wrap-v1"), w.OrganizationID)
	b = lp(lp(append(b, scope), scopeID), w.Scope.KeyID)
	return lp(lp(append(b, 3), ""), w.RecipientKeyID), nil
}

// lp appends a uint16 big-endian length prefix and s.
func lp(b []byte, s string) []byte {
	return append(binary.BigEndian.AppendUint16(b, uint16(len(s))), s...)
}

// keySchedule is RFC 9180 KeySchedule in base mode for the suite
// (0x647a, 0x0001, 0x0002); a single-shot open uses sequence 0, so the nonce
// is base_nonce.
func keySchedule(sharedSecret, info []byte) (cipher.AEAD, []byte, error) {
	suite := []byte("HPKE\x64\x7a\x00\x01\x00\x02")
	extract := func(salt []byte, label string, ikm []byte) []byte {
		prk, _ := hkdf.Extract(sha256.New, append(append(append([]byte("HPKE-v1"), suite...), label...), ikm...), salt)
		return prk
	}
	expand := func(prk []byte, label string, info []byte, n int) ([]byte, error) {
		l := append(append(append(binary.BigEndian.AppendUint16(nil, uint16(n)), "HPKE-v1"...), suite...), label...)
		return hkdf.Expand(sha256.New, prk, string(append(l, info...)), n)
	}
	ctx := append(append([]byte{0}, extract(nil, "psk_id_hash", nil)...), extract(nil, "info_hash", info)...)
	secret := extract(sharedSecret, "secret", nil)
	key, err := expand(secret, "key", ctx, 32)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := expand(secret, "base_nonce", ctx, 12)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	return gcm, nonce, err
}
