package sigverify

import (
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"strings"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

// privKeyPEMType is the PEM type for a WendyOS ML-DSA65 signing private key.
// Kept distinct from the public pinnedKeyPEMType so a private key can never be
// mistaken for (or embedded as) the pinned verification key.
const privKeyPEMType = "WENDY MLDSA65 PRIVATE KEY"

// Signer produces ML-DSA65 detached signatures with a single private key. It is
// the counterpart to Verifier and signs with the same parameters Verify checks
// (a nil context, deterministic), so a signature it makes verifies against the
// matching public key.
type Signer struct {
	priv *mldsa65.PrivateKey
}

// GenerateKeypair creates a fresh ML-DSA65 keypair as PEM: the public key in the
// pinned-key format (embed it as pinned_signing_key.pem to enable verification),
// the private key in the signing format (keep secret; pass to NewSignerFromPEM).
func GenerateKeypair() (pubPEM, privPEM []byte, err error) {
	pub, priv, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("sigverify: generating ML-DSA key: %w", err)
	}
	pubRaw, err := pub.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	privRaw, err := priv.MarshalBinary()
	if err != nil {
		return nil, nil, err
	}
	return marshalPinnedKeyPEM(pubRaw),
		pem.EncodeToMemory(&pem.Block{Type: privKeyPEMType, Bytes: privRaw}), nil
}

// NewSignerFromPEM parses an ML-DSA65 signing private key from PEM.
func NewSignerFromPEM(privPEM []byte) (*Signer, error) {
	if len(strings.TrimSpace(string(privPEM))) == 0 {
		return nil, fmt.Errorf("sigverify: empty signing key")
	}
	block, _ := pem.Decode(privPEM)
	if block == nil || block.Type != privKeyPEMType {
		return nil, fmt.Errorf("sigverify: invalid signing key PEM (want %q)", privKeyPEMType)
	}
	priv := new(mldsa65.PrivateKey)
	if err := priv.UnmarshalBinary(block.Bytes); err != nil {
		return nil, fmt.Errorf("sigverify: parsing signing key: %w", err)
	}
	return &Signer{priv: priv}, nil
}

// Sign returns a detached ML-DSA65 signature over message. Callers sign the
// sha256 digest of the artifact — the same message Verify checks.
func (s *Signer) Sign(message []byte) ([]byte, error) {
	sig := make([]byte, mldsa65.SignatureSize)
	if err := mldsa65.SignTo(s.priv, message, nil, false, sig); err != nil {
		return nil, fmt.Errorf("sigverify: signing: %w", err)
	}
	return sig, nil
}

// PublicKeyPEM returns the signer's public key in the pinned-key PEM format,
// suitable for embedding as pinned_signing_key.pem.
func (s *Signer) PublicKeyPEM() ([]byte, error) {
	pub, ok := s.priv.Public().(*mldsa65.PublicKey)
	if !ok {
		return nil, fmt.Errorf("sigverify: unexpected public key type from private key")
	}
	raw, err := pub.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return marshalPinnedKeyPEM(raw), nil
}
