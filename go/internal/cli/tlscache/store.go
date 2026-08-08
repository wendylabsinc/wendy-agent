// Package tlscache persists TLS 1.3 session tickets across CLI invocations so
// repeat connects to a provisioned agent resume the session instead of redoing
// the full ML-DSA mTLS handshake (~2.2s on device hardware; see
// specs/2026-08-07-tls-session-resumption-design.md).
package tlscache

// A sessionStore persists opaque session blobs by key. Implementations treat
// every failure as a cache miss and never return errors: resumption is an
// optimization whose universal fallback is a full handshake.
type sessionStore interface {
	get(key string) []byte // nil on miss or any error
	put(key string, blob []byte)
	delete(key string)
}
