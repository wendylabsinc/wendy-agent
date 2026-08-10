package config

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

// secretsPlatformDefault is true where the platform default is the
// Keychain. Variable (not const) so non-darwin CI can exercise the
// dehydration paths with a fake store.
var secretsPlatformDefault = true

func defaultCredentialStore() secretStoreIface {
	return secretstore.NewKeychain(credentialService)
}
