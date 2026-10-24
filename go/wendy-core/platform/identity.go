package platform

import (
	"context"
	"net/http"
)

// HTTPTransport sends HTTP requests using host networking and TLS. Request.Context
// carries cancellation and deadlines. The host must not retry token exchanges or
// forward Authorization/DPoP headers across origins. The caller owns and closes
// the response body. Implementations must be safe for concurrent use and keep
// credentials out of logs. Core has no default network implementation.
type HTTPTransport interface {
	Do(request *http.Request) (*http.Response, error)
}

// SignatureVerifier verifies exact JWS signing bytes against a public JWK. The
// host implements the named algorithm (ES256 or ML-DSA-65). It returns an error
// for invalid signatures, unsupported algorithms, malformed keys or cancellation.
// Core validates claims only after successful verification.
type SignatureVerifier interface {
	VerifySignature(ctx context.Context, jwk map[string]string, message, signature []byte) error
}

// IdentityKey adds host-side PKCS#10 creation and certificate key binding to a
// Signer. Private key bytes stay in the host, including for a secondary Lite key.
type IdentityKey interface {
	Signer
	// CertificateRequest creates a PEM PKCS#10 request for subject with this key.
	CertificateRequest(ctx context.Context, subject string) ([]byte, error)
	// CheckCertificate parses a leaf-first PEM chain and refuses a leaf for a
	// different key. It returns normalized PEM and the sole tenant SPIFFE URI.
	// The host owns certificate parsing and cryptographic checks.
	CheckCertificate(ctx context.Context, chain []byte) (CertificateIdentity, error)
}

// CertificateIdentity contains public certificate material checked by the host.
// Core additionally validates tenant, principal kind and subject.
type CertificateIdentity struct {
	CertificatePEM string
	ChainPEM       string
	PrincipalURI   string
}
