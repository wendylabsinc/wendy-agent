// Package platform defines host capabilities used by portable Core code.
// Hosts own private keys, networking, connection security and authentication.
package platform

import "context"

// SignatureAlgorithm identifies a signature scheme by its protocol name.
type SignatureAlgorithm string

// MLDSA65 is the pure ML-DSA-65 signature scheme, without pre-hashing by Core.
const MLDSA65 SignatureAlgorithm = "ML-DSA-65"

// Signer signs with a host-owned key. Private key material never crosses this
// interface. A signer represents one key and algorithm for its entire lifetime;
// certificate renewal may replace its chain but must retain that key.
// Implementations must be safe for concurrent use and keep secrets out of errors.
// All blocking methods honor ctx cancellation/deadlines and return an error
// matching context.Canceled/context.DeadlineExceeded through errors.Is.
type Signer interface {
	// Algorithm returns the signature scheme used by Sign.
	Algorithm() SignatureAlgorithm

	// CertificateChain returns DER certificates, leaf first, or nil when the host
	// supplies only a public key. PublicKey and the leaf, if present, must identify
	// the key used by Sign. Returned bytes are owned by the caller.
	CertificateChain(ctx context.Context) ([][]byte, error)

	// PublicKey returns the DER SubjectPublicKeyInfo for the signing key, or nil
	// when CertificateChain supplies it. At least one form must be available.
	// Returned bytes are owned by the caller.
	PublicKey(ctx context.Context) ([]byte, error)

	// Sign signs exactly message, without additional encoding or pre-hashing,
	// and returns the raw signature. The host honors ctx cancellation/deadlines.
	// It may read message only until Sign returns and must not modify it.
	// The returned signature is owned by the caller.
	Sign(ctx context.Context, message []byte) ([]byte, error)
}
