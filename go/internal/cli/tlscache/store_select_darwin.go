package tlscache

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

// newPlatformStore returns the file backend on macOS, same as everywhere else.
//
// The Keychain backend (secretstore.NewKeychain, still reachable via
// WENDY_TLS_SESSION_STORE=keychain) shells out to `/usr/bin/security`, which
// offers no way to suppress user interaction: `add-generic-password` has no
// no-interaction flag and `security` has no global one. secretstore's
// checkWritableKeychain now closes that hole from the other side — it declines
// the write in the states macOS would answer with a modal — but the file
// backend remains the default here on latency grounds alone: it drops three
// (now five, counting the probes) subprocess spawns per connection from a
// feature whose whole point is speed.
//
// Session resumption is a latency optimization whose fallback is a full
// handshake, so it must never be able to interrupt the user. The file backend
// cannot prompt at all.
//
// The security delta is small enough to accept: the ticket is a 7-day bearer
// secret derived from a client identity whose ML-DSA private key already sits
// unencrypted in ~/.wendy/config.json on macOS (config.CertificateInfo's
// PemPrivateKey), in the same directory tree, at the same 0600 exposure. As
// the design spec's own caveat notes, the Keychain here is "not a stronger
// same-user access boundary than the file backend" — what it buys is at-rest
// encryption while the keychain is locked plus backup exclusion. Users who
// want that can opt in.
func newPlatformStore() secretstore.Store { return newFileStore() }
