package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/term"

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
		return "", resolveError("", fmt.Errorf("unrecognized credential reference %q (written by a newer wendy?)", ref))
	}
	store := newCredentialStore()
	if store == nil {
		return "", resolveError(account, fmt.Errorf("no credential store on this platform (config migrated on macOS?)"))
	}
	secret := store.Get(account)
	if secret == nil {
		return "", resolveError(account, fmt.Errorf("keychain item %s/%s not readable", credentialService, account))
	}
	cacheSecret(ref, string(secret))
	return string(secret), nil
}

// resolveError wraps a failed lookup with the remedies. It names the probe
// command as well: security(1) reports "locked", "not found" and "user
// interaction is not allowed" as the same miss here, and which one it is
// decides between unlocking and logging in again.
func resolveError(account string, cause error) error {
	probe := "security find-generic-password -s " + credentialService
	if account != "" {
		probe += " -a " + account
	}
	return fmt.Errorf("credential is stored in the macOS Keychain but could not be read (keychain locked?): %w\n"+
		"Diagnose with '%s -w'.\n"+
		"Unlock with 'security unlock-keychain', or re-run 'wendy auth login' with WENDY_SECRET_STORE=file to keep credentials in config.json.", cause, probe)
}

// keyAccount derives the deterministic Keychain account for a client
// private key, so re-login for the same identity overwrites one item. It
// hashes cloudGRPC|orgID|userID|assetID: userID alone is not enough because
// asset certs (from performLocalLogin) carry no UserID, only an AssetID, so
// two asset certs on the same endpoint+org with different AssetID must not
// collide on the same Keychain item.
func keyAccount(cloudGRPC string, orgID int, userID string, assetID int) string {
	sum := sha256.Sum256([]byte(cloudGRPC + "|" + strconv.Itoa(orgID) + "|" + userID + "|" + strconv.Itoa(assetID)))
	return "key-" + hex.EncodeToString(sum[:8])
}

// tokenAccount derives the deterministic Keychain account for a cloud API
// token. It hashes cloudDashboard|cloudGRPC|orgID — the same triple AddAuth
// dedups auth entries on — because a browser login (CloudDashboard set) and
// an --api-key login (CloudDashboard empty, see performLocalLogin) against
// the same endpoint+org coexist as two distinct AddAuth entries; omitting
// cloudDashboard would let one entry's dehydrated token overwrite the
// other's Keychain item even though both entries' references kept pointing
// at the same account. orgID matters for the analogous reason: several orgs
// can share one endpoint (e.g. multiple orgs on the production cloud), each
// with its own AddAuth entry.
func tokenAccount(cloudDashboard, cloudGRPC string, orgID int) string {
	sum := sha256.Sum256([]byte(cloudDashboard + "|" + cloudGRPC + "|" + strconv.Itoa(orgID)))
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

// dehydrateEnabled reports whether Save should move inline secrets into the
// platform store.
//
// WENDY_SECRET_STORE pins the answer either way: "file" forces inline writes
// (and de-migration), "keychain" forces the store on even where the default
// below declines.
//
// Otherwise the Keychain is used only for invocations a person is sitting in
// front of. A Keychain item is readable only while the login keychain is
// unlocked, and unlocking it is an interactive act — so migrating a headless
// macOS host (a CI runner, a launchd job, `ssh host wendy ...`) swaps a
// config.json that always works for one that stops working the moment that
// keychain locks, with no copy left on disk to fall back to and no way to
// answer the unlock prompt. That is not hypothetical: it took the macOS
// integration-test runner offline, and with it every release, because the
// runner's only client key had moved into a keychain no non-interactive
// session could open.
func dehydrateEnabled() bool {
	switch os.Getenv("WENDY_SECRET_STORE") {
	case "file":
		return false
	case "keychain":
		return secretsPlatformDefault
	}
	return secretsPlatformDefault && interactiveSession()
}

// interactiveSession reports whether someone could answer a Keychain prompt
// for this process. Package variable so tests pin it rather than inheriting
// whatever `go test` was launched from.
//
// stdin and stderr are the streams that matter: stdout is redirected by
// ordinary interactive use (`wendy device list | less`, `--json > file`),
// while a piped stdin means nobody is there to type. CI is checked too, but
// only as a belt-and-braces signal — it does not survive an `ssh host wendy
// ...` hop, which is exactly how the integration-test runner invokes the CLI,
// so the terminal check is what actually carries that case.
var interactiveSession = func() bool {
	if os.Getenv("CI") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// clone deep-copies a Config via JSON round-trip so Save can rewrite secret
// fields without mutating the caller's struct. An error here must not fall
// back to returning c itself — that would silently hand Save the caller's
// live struct and violate the never-mutate contract on the very path that
// handles secrets.
func (c *Config) clone() (*Config, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// dehydrate pushes every inline secret into the credential store and
// replaces the field with its reference. A failed Put keeps the value
// inline — the store never holds the only copy until a write succeeded.
func dehydrate(cfg *Config) {
	store := newCredentialStore()
	if store == nil {
		return
	}
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		if a.APIKey != "" && !isRef(a.APIKey) {
			acct := tokenAccount(a.CloudDashboard, a.CloudGRPC, authEntryOrgID(*a))
			if store.Put(acct, []byte(a.APIKey)) == nil {
				cacheSecret(refPrefixV1+acct, a.APIKey)
				a.APIKey = refPrefixV1 + acct
			}
		}
		for j := range a.Certificates {
			c := &a.Certificates[j]
			if c.PemPrivateKey != "" && !isRef(c.PemPrivateKey) {
				acct := keyAccount(a.CloudGRPC, c.OrganizationID, c.UserID, c.AssetID)
				if store.Put(acct, []byte(c.PemPrivateKey)) == nil {
					cacheSecret(refPrefixV1+acct, c.PemPrivateKey)
					c.PemPrivateKey = refPrefixV1 + acct
				}
			}
		}
	}
}

// inlineSecrets is the de-migration path (WENDY_SECRET_STORE=file):
// references that resolve are written back inline; unresolvable references
// are kept — never drop a secret.
func inlineSecrets(cfg *Config) {
	for i := range cfg.Auth {
		a := &cfg.Auth[i]
		if isRef(a.APIKey) {
			if v, err := resolveSecret(a.APIKey); err == nil {
				a.APIKey = v
			}
		}
		for j := range a.Certificates {
			c := &a.Certificates[j]
			if isRef(c.PemPrivateKey) {
				if v, err := resolveSecret(c.PemPrivateKey); err == nil {
					c.PemPrivateKey = v
				}
			}
		}
	}
}

// hasInlineSecrets reports whether any secret field holds a real value
// (as opposed to a reference or empty).
func hasInlineSecrets(cfg *Config) bool {
	return countInlineSecrets(cfg) > 0
}

// MigrateSecretsIfNeeded moves pre-existing plaintext secrets into the
// platform store. Called once per invocation from the root command's
// synchronous pre-run zone; organic Saves elsewhere migrate silently, so
// this hook exists to (a) migrate users who never run a config-saving
// command and (b) own the one-line notice. Returns true when a migration
// actually reduced the number of inline secrets on disk.
func MigrateSecretsIfNeeded(cfg *Config) bool {
	if !dehydrateEnabled() || !hasInlineSecrets(cfg) {
		return false
	}
	if err := Save(cfg); err != nil {
		return false
	}
	reloaded, err := Load()
	if err != nil {
		return false
	}
	return countInlineSecrets(reloaded) < countInlineSecrets(cfg)
}

// RestoreSecretsIfNeeded is MigrateSecretsIfNeeded's mirror: it pulls
// Keychain-referenced secrets back inline once the store is no longer in use
// here — WENDY_SECRET_STORE=file, or a host that dehydrateEnabled now
// declines to migrate (headless, non-interactive). Without it a machine that
// migrated while someone was logged in stays pinned to the Keychain forever,
// because nothing else on a read-only command path calls Save.
//
// Returns true only when references were actually resolved and written back.
func RestoreSecretsIfNeeded(cfg *Config) bool {
	if dehydrateEnabled() {
		return false
	}
	before := countSecretRefs(cfg)
	if before == 0 {
		return false
	}
	// Resolve against a throwaway copy first. On a locked or emptied keychain
	// every reference is a miss and Save would rewrite the same bytes on every
	// command forever; this keeps the rewrite to runs that recover something.
	// The lookups it performs are memoized, so Save's own pass is free.
	probe, err := cfg.clone()
	if err != nil {
		return false
	}
	inlineSecrets(probe)
	if countSecretRefs(probe) == before {
		return false
	}
	if err := Save(cfg); err != nil {
		return false
	}
	return true
}

func countSecretRefs(cfg *Config) int {
	n := 0
	for _, a := range cfg.Auth {
		if isRef(a.APIKey) {
			n++
		}
		for _, c := range a.Certificates {
			if isRef(c.PemPrivateKey) {
				n++
			}
		}
	}
	return n
}

func countInlineSecrets(cfg *Config) int {
	n := 0
	for _, a := range cfg.Auth {
		if a.APIKey != "" && !isRef(a.APIKey) {
			n++
		}
		for _, c := range a.Certificates {
			if c.PemPrivateKey != "" && !isRef(c.PemPrivateKey) {
				n++
			}
		}
	}
	return n
}

// DeleteStoredSecrets removes every Keychain item this config references —
// called when credentials are being discarded (logout). Best-effort: an
// orphaned item is inert once nothing references it, but tidy-up is cheap.
func DeleteStoredSecrets(cfg *Config) {
	store := newCredentialStore()
	if store == nil {
		return
	}
	deleteRef := func(ref string) {
		account, ok := strings.CutPrefix(ref, refPrefixV1)
		if !ok {
			return
		}
		store.Delete(account)
		secretMu.Lock()
		delete(secretCache, ref)
		secretMu.Unlock()
	}
	for _, a := range cfg.Auth {
		if isRef(a.APIKey) {
			deleteRef(a.APIKey)
		}
		for _, c := range a.Certificates {
			if isRef(c.PemPrivateKey) {
				deleteRef(c.PemPrivateKey)
			}
		}
	}
}
