package tlscache

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

// newPlatformStore returns the file backend on macOS, same as everywhere else.
//
// The Keychain backend (secretstore.NewKeychain, still reachable via
// WENDY_TLS_SESSION_STORE=keychain) shells out to `/usr/bin/security`, which
// offers no way to suppress user interaction: `add-generic-password` has no
// no-interaction flag and `security` has no global one. In any context where
// the keychain search list does not resolve — a sandboxed process, a non-login
// session — macOS answers the write with a blocking "A keychain cannot be
// found to store ..." modal. Put runs on a background goroutine and discards
// its result, so that modal appears with no CLI context and nothing to
// correlate it to.
//
// Session resumption is a latency optimization whose fallback is a full
// handshake, so it must never be able to interrupt the user. The file backend
// cannot prompt. It also drops three subprocess spawns per connection from a
// feature whose whole point is speed.
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
