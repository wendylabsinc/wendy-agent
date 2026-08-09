// Package tlscache persists TLS 1.3 session tickets across CLI invocations so
// repeat connects to a provisioned agent resume the session instead of redoing
// the full ML-DSA mTLS handshake (~2.2s on device hardware; see
// specs/2026-08-07-tls-session-resumption-design.md).
package tlscache

import (
	"os"

	"github.com/wendylabsinc/wendy/go/internal/shared/secretstore"
)

// keychainService names the Keychain items holding wendy session tickets.
const keychainService = "wendy-tls-session"

// newDefaultStore picks the ticket store backend. WENDY_TLS_SESSION_STORE
// forces one: "off" disables caching (right for CI), "file"/"keychain" force a
// backend. Anything else gets the platform default, which is the file backend
// on every platform — "keychain" is opt-in only, because it costs several
// `security` subprocesses per connection (see newPlatformStore in
// store_select_darwin.go). A nil return disables session caching entirely.
func newDefaultStore() secretstore.Store {
	switch os.Getenv("WENDY_TLS_SESSION_STORE") {
	case "off":
		return nil
	case "file":
		return newFileStore()
	case "keychain":
		if s := secretstore.NewKeychain(keychainService); s != nil {
			return s
		}
		return newFileStore() // no keychain backend on this platform
	}
	return newPlatformStore()
}
