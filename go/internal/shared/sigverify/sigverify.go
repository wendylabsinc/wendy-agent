// Package sigverify verifies ML-DSA65 detached signatures over release
// artifacts (agent binary, container image digest) against a pinned public
// key embedded at build time. Verification is enforced only when a
// non-empty key is embedded; otherwise it skips (fail-safe) so unsigned/dev
// builds keep working.
package sigverify

import (
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudflare/circl/sign/mldsa/mldsa65"
)

const pinnedKeyPEMType = "WENDY MLDSA65 PUBLIC KEY"

var (
	// ErrUnsigned is returned by Verify when a pinned key is present (the
	// verifier is enabled) but no signature was supplied.
	ErrUnsigned = errors.New("sigverify: artifact is unsigned but a pinned key is present")
	// ErrBadSignature is returned by Verify when a pinned key is present and
	// the supplied signature does not verify against the message.
	ErrBadSignature = errors.New("sigverify: signature verification failed")
)

// Verifier checks ML-DSA65 detached signatures against a single pinned
// public key. The zero value (and any Verifier built from an empty key) is
// disabled: Verify always succeeds so unsigned/dev builds keep working.
type Verifier struct {
	pub *mldsa65.PublicKey // nil => disabled
}

// marshalPinnedKeyPEM encodes a raw ML-DSA65 public key as a PEM block using
// the pinned-key PEM type recognized by NewVerifier.
func marshalPinnedKeyPEM(raw []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: pinnedKeyPEMType, Bytes: raw})
}

// NewVerifier parses an ML-DSA65 public key from PEM. If pinnedKeyPEM is
// empty or contains only whitespace, it returns a disabled Verifier
// (Enabled() == false) whose Verify calls always skip.
func NewVerifier(pinnedKeyPEM []byte) (*Verifier, error) {
	if len(strings.TrimSpace(string(pinnedKeyPEM))) == 0 {
		return &Verifier{}, nil // disabled
	}
	block, _ := pem.Decode(pinnedKeyPEM)
	if block == nil || block.Type != pinnedKeyPEMType {
		return nil, fmt.Errorf("sigverify: invalid pinned key PEM")
	}
	pub := new(mldsa65.PublicKey)
	if err := pub.UnmarshalBinary(block.Bytes); err != nil {
		return nil, fmt.Errorf("sigverify: parsing pinned ML-DSA key: %w", err)
	}
	return &Verifier{pub: pub}, nil
}

// Enabled reports whether a non-empty pinned key was embedded, i.e. whether
// Verify actually enforces signature checks.
func (v *Verifier) Enabled() bool { return v != nil && v.pub != nil }

// Verify checks signature (a detached ML-DSA65 signature) over message.
//
// If the verifier is disabled (no pinned key embedded), Verify always
// returns nil (fail-safe skip). When enabled, it returns ErrUnsigned if
// signature is empty, ErrBadSignature if verification fails, or nil if the
// signature is valid.
func (v *Verifier) Verify(message, signature []byte) error {
	if !v.Enabled() {
		return nil // fail-safe skip
	}
	if len(signature) == 0 {
		return ErrUnsigned
	}
	if !mldsa65.Verify(v.pub, message, nil, signature) {
		return ErrBadSignature
	}
	return nil
}
