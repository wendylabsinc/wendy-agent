// Package secretstore provides platform secret storage for the wendy CLI.
// The macOS backend shells out to /usr/bin/security (the same pattern as
// wifi_scan_darwin.go's lookupKeychainPassword); other platforms have no
// backend yet — NewKeychain returns nil there and callers fall back to
// their own storage (inline config fields, 0600 files).
package secretstore

// Store persists opaque secrets by account name. Get treats every failure
// as a miss; Put reports failure so callers holding the only copy of a
// secret can refuse to discard it; Delete is best-effort. Service and
// account strings must contain no whitespace or quotes — the macOS backend
// interpolates them into a `security -i` command line; wendy's callers
// satisfy this by using fixed service names and hex/base64-encoded accounts.
type Store interface {
	Get(account string) []byte
	Put(account string, secret []byte) error
	Delete(account string)
}
