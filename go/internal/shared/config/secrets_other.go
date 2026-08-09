//go:build !darwin

package config

// secretsPlatformDefault: no secret-store backend off darwin; secrets stay
// inline. Variable so tests can exercise dehydration with a fake store.
var secretsPlatformDefault = false

func defaultCredentialStore() secretStoreIface { return nil }
