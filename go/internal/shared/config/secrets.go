package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/shared/secretstore"
)

// credentialService names the Keychain items holding wendy credentials —
// distinct from tlscache's "wendy-tls-session" (tickets are droppable,
// credentials are not, so the two keep separate policy).
const credentialService = "wendy-credentials"

// Secret fields in config.json hold either a real value or a reference of
// this form. Values never collide with references: private keys are PEM
// ("-----BEGIN...") and API tokens are opaque strings that do not start
// with "keychain:".
const (
	refPrefix   = "keychain:"
	refPrefixV1 = "keychain:v1:"
)

// secretStoreIface aliases the store interface so tests can swap fakes
// without importing secretstore.
type secretStoreIface = secretstore.Store

// newCredentialStore returns the platform credential store (nil = no
// backend, secrets stay inline). Package variable so tests inject fakes.
var newCredentialStore = defaultCredentialStore

var (
	secretMu    sync.Mutex
	secretCache = map[string]string{} // ref → resolved secret, per process
)

func resetSecretCacheForTest() {
	secretMu.Lock()
	defer secretMu.Unlock()
	secretCache = map[string]string{}
}

func cacheSecret(ref, value string) {
	secretMu.Lock()
	defer secretMu.Unlock()
	secretCache[ref] = value
}

func isRef(v string) bool { return strings.HasPrefix(v, refPrefix) }

// resolveSecret turns a keychain reference into the stored secret,
// memoized per process so repeated config loads cost one security(1)
// invocation per secret.
func resolveSecret(ref string) (string, error) {
	secretMu.Lock()
	if v, ok := secretCache[ref]; ok {
		secretMu.Unlock()
		return v, nil
	}
	secretMu.Unlock()

	account, ok := strings.CutPrefix(ref, refPrefixV1)
	if !ok {
		return "", resolveError(fmt.Errorf("unrecognized credential reference %q (written by a newer wendy?)", ref))
	}
	store := newCredentialStore()
	if store == nil {
		return "", resolveError(fmt.Errorf("no credential store on this platform (config migrated on macOS?)"))
	}
	secret := store.Get(account)
	if secret == nil {
		return "", resolveError(fmt.Errorf("keychain item %s/%s not readable", credentialService, account))
	}
	cacheSecret(ref, string(secret))
	return string(secret), nil
}

func resolveError(cause error) error {
	return fmt.Errorf("credential is stored in the macOS Keychain but could not be read (keychain locked?): %w\n"+
		"Unlock with 'security unlock-keychain', or re-run 'wendy auth login' with WENDY_SECRET_STORE=file to keep credentials in config.json.", cause)
}

// keyAccount derives the deterministic Keychain account for a client
// private key, so re-login for the same identity overwrites one item.
func keyAccount(cloudGRPC string, orgID int, userID string) string {
	sum := sha256.Sum256([]byte(cloudGRPC + "|" + strconv.Itoa(orgID) + "|" + userID))
	return "key-" + hex.EncodeToString(sum[:8])
}

func tokenAccount(cloudGRPC string) string {
	sum := sha256.Sum256([]byte(cloudGRPC))
	return "token-" + hex.EncodeToString(sum[:8])
}

// HasPrivateKey reports whether key material exists — inline or by
// reference — without touching the Keychain.
func (c CertificateInfo) HasPrivateKey() bool { return c.PemPrivateKey != "" }

// PrivateKeyPEM returns the client private key, resolving a Keychain
// reference if necessary.
func (c CertificateInfo) PrivateKeyPEM() (string, error) {
	if !isRef(c.PemPrivateKey) {
		return c.PemPrivateKey, nil
	}
	return resolveSecret(c.PemPrivateKey)
}

// HasAPIKey reports whether an API token exists — inline or by reference —
// without touching the Keychain.
func (a AuthConfig) HasAPIKey() bool { return a.APIKey != "" }

// BearerToken returns the cloud API token, resolving a Keychain reference
// if necessary.
func (a AuthConfig) BearerToken() (string, error) {
	if !isRef(a.APIKey) {
		return a.APIKey, nil
	}
	return resolveSecret(a.APIKey)
}
