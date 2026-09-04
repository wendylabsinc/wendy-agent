package config

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

// Keep credentials in config.json by default. The Keychain backend remains
// available to resolve references written by older CLI versions so startup can
// migrate readable credentials back into the file without requiring a login.
// Variable (not const) so tests can exercise both storage policies.
var secretsPlatformDefault = false

func defaultCredentialStore() secretStoreIface {
	return secretstore.NewKeychain(credentialService)
}
