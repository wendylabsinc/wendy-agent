//go:build !darwin

package tlscache

func newPlatformStore() sessionStore { return newFileStore() }

// newKeychainStore has no non-darwin implementation; an explicit
// WENDY_TLS_SESSION_STORE=keychain falls back to files rather than failing.
func newKeychainStore() sessionStore { return newFileStore() }
