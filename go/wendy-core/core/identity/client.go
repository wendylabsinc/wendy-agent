// Package identity implements shared Wendy sign-in and session protocols.
// Hosts retain private keys, networking, signature verification, secret storage,
// refresh locking, browser interaction and presentation.
package identity

import "github.com/wendylabsinc/WendyOS/go/wendy-core/platform"

// Client uses host-supplied capabilities. Callers serialize mutations of the
// same session, including across processes sharing a refresh-token family.
type Client struct {
	HTTP     platform.HTTPTransport
	Verifier platform.SignatureVerifier
}
