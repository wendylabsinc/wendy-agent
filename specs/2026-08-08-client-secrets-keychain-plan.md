# Client Secrets in macOS Keychain — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the CLI's two plaintext secrets in `~/.wendy/config.json` — the per-org ML-DSA private key and the cloud API bearer token — into the macOS Keychain, with silent auto-migration and lazy memoized accessors.

**Architecture:** A shared `go/internal/shared/secretstore` package (Keychain store + `security` runner promoted out of `tlscache`); `config.json` secret fields hold `keychain:v1:<account>` references; reads go through accessors that resolve + memoize; `Save()` dehydrates (which IS the migration); a root-command hook triggers migration once and prints the notice. Spec: `specs/2026-08-08-client-secrets-keychain-design.md`.

**Tech Stack:** Go 1.26, `/usr/bin/security` subprocess (stdin-only writes), cobra root hook.

## Global Constraints

- Branch `ed/client-key-keychain` is **stacked on `ed/tls-session-resumption`**; the PR targets that branch, not main.
- Module root is the repo root; packages under `go/...`; run `go` commands from the repo root. Repo-root `docs/` is a symlink into embedded CLI assets — docs go in top-level `specs/`.
- `secretstore` lives under `go/internal/shared/` — the `config` package is shared and must never import `go/internal/cli/...`.
- Ref format exactly `keychain:v1:<account>`; inline-vs-ref detection is `strings.HasPrefix(v, "keychain:")`; an unknown `keychain:` version fails with the same actionable error as a failed read.
- Keychain service for credentials exactly `wendy-credentials`; accounts `key-<hex16>` / `token-<hex16>` (first 16 hex chars = 8 bytes of SHA-256).
- Secrets must NEVER be lost: a failed Keychain `Put` during dehydration keeps the value inline; a failed read during de-migration keeps the reference.
- Secrets never on argv: Keychain writes ride stdin via `security -i` (existing pattern).
- Resolution is memoized per process: N accessor calls for one secret = 1 `security` invocation.
- `WENDY_SECRET_STORE=file` = inline-only writes (also the de-migration path); unset = platform default (Keychain on darwin, inline elsewhere); any other value behaves like the platform default. The env NEVER affects ref *resolution* — existing refs always resolve.
- Non-darwin behavior stays byte-identical to today (inline plaintext).
- Actionable error text (verbatim, two lines):
  `credential is stored in the macOS Keychain but could not be read (keychain locked?): <cause>`
  `Unlock with 'security unlock-keychain', or re-run 'wendy auth login' with WENDY_SECRET_STORE=file to keep credentials in config.json.`
- Commit messages end with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`
- Before any push: `gofmt -l .` from repo root must be empty.

---

### Task 1: `secretstore` package — promote the Keychain store out of `tlscache`

**Files:**
- Create: `go/internal/shared/secretstore/store.go`
- Create: `go/internal/shared/secretstore/keychain_darwin.go`
- Create: `go/internal/shared/secretstore/keychain_other.go`
- Test: `go/internal/shared/secretstore/keychain_darwin_test.go`
- Modify: `go/internal/cli/tlscache/store.go` (interface → `secretstore.Store`)
- Modify: `go/internal/cli/tlscache/store_file.go` (method names `get/put/delete` → `Get/Put/Delete`; `put` gains an error return)
- Modify: `go/internal/cli/tlscache/store_select_darwin.go`, `store_select_other.go`
- Delete: `go/internal/cli/tlscache/store_keychain_darwin.go` (moves here)
- Modify: `go/internal/cli/tlscache/cache.go` (call sites `c.store.get(...)` → `c.store.Get(...)` etc.)
- Modify: `go/internal/cli/tlscache/store_keychain_darwin_test.go` → delete (its coverage moves to the new secretstore test); `store_select_test.go`, `store_file_test.go`, `cache_test.go` updated for the renamed methods.

**Interfaces:**
- Consumes: existing `tlscache` code from PR #1612 (this branch's base).
- Produces (relied on by Tasks 2-7):
  ```go
  package secretstore
  type Store interface {
      Get(account string) []byte              // nil on miss or any error
      Put(account string, secret []byte) error // error so callers can avoid losing a secret
      Delete(account string)
  }
  func NewKeychain(service string) Store // darwin: Keychain-backed; other platforms: nil
  var RunSecurity func(ctx context.Context, stdin string, args ...string) ([]byte, error) // swapped by tests
  ```

- [ ] **Step 1: Write the failing secretstore test**

`go/internal/shared/secretstore/keychain_darwin_test.go` — port of the existing tlscache keychain tests, parameterized by service:

```go
package secretstore

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

type fakeSecurity struct {
	calls []struct {
		stdin string
		args  []string
	}
	out []byte
	err error
}

func (f *fakeSecurity) run(_ context.Context, stdin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, struct {
		stdin string
		args  []string
	}{stdin, args})
	return f.out, f.err
}

func withFake(t *testing.T, f *fakeSecurity) {
	t.Helper()
	orig := RunSecurity
	RunSecurity = f.run
	t.Cleanup(func() { RunSecurity = orig })
}

func TestKeychainGetDecodesBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte(base64.StdEncoding.EncodeToString([]byte("blob")) + "\n")}
	withFake(t, fake)
	got := NewKeychain("svc-a").Get("acct1")
	if string(got) != "blob" {
		t.Fatalf("Get = %q, want blob", got)
	}
	want := "find-generic-password -s svc-a -a acct1 -w"
	if strings.Join(fake.calls[0].args, " ") != want {
		t.Errorf("args = %v, want %q", fake.calls[0].args, want)
	}
}

func TestKeychainGetMissOrDenied(t *testing.T) {
	fake := &fakeSecurity{err: errors.New("exit status 44")}
	withFake(t, fake)
	if got := NewKeychain("svc-a").Get("acct1"); got != nil {
		t.Errorf("Get on error = %q, want nil", got)
	}
}

func TestKeychainGetBadBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte("!!! not base64 !!!")}
	withFake(t, fake)
	if got := NewKeychain("svc-a").Get("acct1"); got != nil {
		t.Errorf("Get on undecodable payload = %q, want nil", got)
	}
}

func TestKeychainPutKeepsSecretOffArgvAndReportsError(t *testing.T) {
	fake := &fakeSecurity{}
	withFake(t, fake)
	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	call := fake.calls[0]
	if strings.Join(call.args, " ") != "-i" {
		t.Fatalf("argv = %v, want [-i]", call.args)
	}
	b64 := base64.StdEncoding.EncodeToString([]byte("secret"))
	for _, frag := range []string{"add-generic-password", "-U", "-s svc-a", "-a acct1", "-w " + b64} {
		if !strings.Contains(call.stdin, frag) {
			t.Errorf("stdin %q missing %q", call.stdin, frag)
		}
	}

	failing := &fakeSecurity{err: errors.New("keychain locked")}
	withFake(t, failing)
	if err := NewKeychain("svc-a").Put("acct1", []byte("secret")); err == nil {
		t.Error("Put with failing security = nil error, want non-nil")
	}
}

func TestKeychainDelete(t *testing.T) {
	fake := &fakeSecurity{}
	withFake(t, fake)
	NewKeychain("svc-a").Delete("acct1")
	want := "delete-generic-password -s svc-a -a acct1"
	if strings.Join(fake.calls[0].args, " ") != want {
		t.Errorf("args = %v, want %q", fake.calls[0].args, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/secretstore/ -v`
Expected: FAIL to build — package does not exist.

- [ ] **Step 3: Implement secretstore**

`go/internal/shared/secretstore/store.go`:

```go
// Package secretstore provides platform secret storage for the wendy CLI.
// The macOS backend shells out to /usr/bin/security (the same pattern as
// wifi_scan_darwin.go's lookupKeychainPassword); other platforms have no
// backend yet — NewKeychain returns nil there and callers fall back to
// their own storage (inline config fields, 0600 files).
package secretstore

// Store persists opaque secrets by account name. Get treats every failure
// as a miss; Put reports failure so callers holding the only copy of a
// secret can refuse to discard it; Delete is best-effort.
type Store interface {
	Get(account string) []byte
	Put(account string, secret []byte) error
	Delete(account string)
}
```

`go/internal/shared/secretstore/keychain_darwin.go` — the moved tlscache code, service parameterized, `Put` returning the runner's error:

```go
package secretstore

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const securityTimeout = 5 * time.Second

// RunSecurity invokes /usr/bin/security. Package-level so tests fake it.
var RunSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}

// keychain stores secrets in the user's login Keychain under one service
// name. Items are created by (and read back through) /usr/bin/security
// itself, whose default ACL covers it — reads must never prompt; any
// prompt/denial surfaces as a plain miss.
type keychain struct{ service string }

// NewKeychain returns a Keychain-backed Store scoped to the given service.
func NewKeychain(service string) Store { return keychain{service: service} }

func (k keychain) Get(account string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	out, err := RunSecurity(ctx, "", "find-generic-password", "-s", k.service, "-a", account, "-w")
	if err != nil {
		return nil // not found, denied, or security failed — miss
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	return blob
}

func (k keychain) Put(account string, secret []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	// `security -i` reads the command from stdin so the secret never appears
	// on argv (argv is world-readable via ps). base64 and account names
	// contain no whitespace, so no quoting is needed.
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -j wendy-cli-secret -w %s\n",
		k.service, account, base64.StdEncoding.EncodeToString(secret))
	_, err := RunSecurity(ctx, cmdLine, "-i")
	return err
}

func (k keychain) Delete(account string) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = RunSecurity(ctx, "", "delete-generic-password", "-s", k.service, "-a", account)
}
```

`go/internal/shared/secretstore/keychain_other.go`:

```go
//go:build !darwin

package secretstore

import "context"

// RunSecurity exists on all platforms so shared test helpers compile; it is
// only ever invoked by the darwin backend.
var RunSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	return nil, nil
}

// NewKeychain has no non-darwin backend; callers must handle nil.
func NewKeychain(service string) Store { return nil }
```

- [ ] **Step 4: Refactor tlscache onto secretstore**

In `go/internal/cli/tlscache/`:
- `store.go`: delete the `sessionStore` interface; the store type used throughout becomes `secretstore.Store` (import `github.com/wendylabsinc/wendy/go/internal/shared/secretstore`). `newDefaultStore() secretstore.Store` keeps its exact `WENDY_TLS_SESSION_STORE` switch; the `"keychain"` case becomes:
  ```go
  case "keychain":
      if s := secretstore.NewKeychain(keychainService); s != nil {
          return s
      }
      return newFileStore() // no keychain backend on this platform
  ```
  Move `const keychainService = "wendy-tls-session"` into `store.go` (it lived in the deleted darwin file).
- `store_select_darwin.go`: `func newPlatformStore() secretstore.Store { return secretstore.NewKeychain(keychainService) }`
- `store_select_other.go` (`//go:build !darwin`): `func newPlatformStore() secretstore.Store { return newFileStore() }` — delete its now-unneeded `newKeychainStore` stub.
- `store_file.go`: rename methods to `Get`/`Put`/`Delete`; `Put(key string, blob []byte) error` returns the first error it currently swallows (each early `return` becomes `return err`; the success path returns the result of `s.prune()`… no — keep `prune()` error-free and `return nil` after it; only the write/rename path errors).
- Delete `store_keychain_darwin.go` and `store_keychain_darwin_test.go` (coverage now lives in secretstore; keep `TestDarwinDefaultIsKeychain` by moving it into `store_select_test.go` with a type assertion against `secretstore.NewKeychain` comparability — simplest: assert `newDefaultStore()` is NOT `*fileStore` on darwin).
- `cache.go`: `c.store.get(` → `c.store.Get(`, `c.store.put(` → `_ = c.store.Put(` (the cache treats persist failure as a dropped ticket — harmless), `c.store.delete(` → `c.store.Delete(`. Field type `store secretstore.Store`.
- Tests: update `memStore` in `cache_test.go` and any fake in `store_select_test.go`/`store_file_test.go` to the exported method set (`Put` returns `error`).

- [ ] **Step 5: Run both packages' suites**

Run: `go test ./go/internal/shared/secretstore/ ./go/internal/cli/tlscache/ -count=1` then `GOOS=linux go build ./go/internal/cli/tlscache/ ./go/internal/shared/secretstore/`
Expected: all PASS; linux cross-build clean.

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/secretstore/ go/internal/cli/tlscache/
git commit -m "refactor(secretstore): promote Keychain store out of tlscache for shared use

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Config references + lazy memoized accessors

**Files:**
- Create: `go/internal/shared/config/secrets.go`
- Create: `go/internal/shared/config/secrets_darwin.go`
- Create: `go/internal/shared/config/secrets_other.go`
- Test: `go/internal/shared/config/secrets_test.go`

**Interfaces:**
- Consumes: `secretstore.Store`, `secretstore.NewKeychain` (Task 1).
- Produces (relied on by Tasks 3-7):
  ```go
  const credentialService = "wendy-credentials"
  const refPrefix = "keychain:"
  const refPrefixV1 = "keychain:v1:"
  func (c CertificateInfo) HasPrivateKey() bool
  func (c CertificateInfo) PrivateKeyPEM() (string, error)
  func (a AuthConfig) HasAPIKey() bool
  func (a AuthConfig) BearerToken() (string, error)
  func keyAccount(cloudGRPC string, orgID int, userID string) string   // "key-<hex16>"
  func tokenAccount(cloudGRPC string) string                            // "token-<hex16>"
  func isRef(v string) bool
  func resolveSecret(ref string) (string, error)
  func cacheSecret(ref, value string)
  var newCredentialStore func() secretstore.Store  // test seam
  var secretsPlatformDefault bool                  // darwin: true; test seam for non-darwin CI
  ```

- [ ] **Step 1: Write the failing tests**

`go/internal/shared/config/secrets_test.go`:

```go
package config

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeStore is an in-memory secretstore.Store counting reads.
type fakeStore struct {
	mu      sync.Mutex
	m       map[string][]byte
	gets    int
	putErr  error
	deletes []string
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string][]byte{}} }

func (s *fakeStore) Get(account string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	return s.m[account]
}

func (s *fakeStore) Put(account string, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	s.m[account] = secret
	return nil
}

func (s *fakeStore) Delete(account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, account)
	s.deletes = append(s.deletes, account)
}

// useFakeStore swaps in a fake credential store and clears the memoization
// cache; restores both on cleanup.
func useFakeStore(t *testing.T, s *fakeStore) {
	t.Helper()
	origNew := newCredentialStore
	newCredentialStore = func() secretStoreIface { return s }
	resetSecretCacheForTest()
	t.Cleanup(func() {
		newCredentialStore = origNew
		resetSecretCacheForTest()
	})
}

func TestAccessorsInlineValues(t *testing.T) {
	useFakeStore(t, newFakeStore())
	c := CertificateInfo{PemPrivateKey: "-----BEGIN PRIVATE KEY-----\nabc"}
	got, err := c.PrivateKeyPEM()
	if err != nil || got != c.PemPrivateKey {
		t.Fatalf("inline PrivateKeyPEM = %q, %v", got, err)
	}
	a := AuthConfig{APIKey: "tok-123"}
	tok, err := a.BearerToken()
	if err != nil || tok != "tok-123" {
		t.Fatalf("inline BearerToken = %q, %v", tok, err)
	}
	if !c.HasPrivateKey() || !a.HasAPIKey() {
		t.Error("Has* = false for inline values")
	}
	if (CertificateInfo{}).HasPrivateKey() || (AuthConfig{}).HasAPIKey() {
		t.Error("Has* = true for empty values")
	}
}

func TestAccessorsResolveRefsMemoized(t *testing.T) {
	store := newFakeStore()
	store.m["key-abc"] = []byte("PEMDATA")
	useFakeStore(t, store)
	c := CertificateInfo{PemPrivateKey: refPrefixV1 + "key-abc"}
	for i := 0; i < 5; i++ {
		got, err := c.PrivateKeyPEM()
		if err != nil || got != "PEMDATA" {
			t.Fatalf("resolve #%d = %q, %v", i, got, err)
		}
	}
	if store.gets != 1 {
		t.Errorf("store reads = %d, want 1 (memoized)", store.gets)
	}
	if !c.HasPrivateKey() {
		t.Error("HasPrivateKey = false for a reference")
	}
}

func TestAccessorErrorTextActionable(t *testing.T) {
	useFakeStore(t, newFakeStore()) // empty store → miss
	c := CertificateInfo{PemPrivateKey: refPrefixV1 + "key-missing"}
	_, err := c.PrivateKeyPEM()
	if err == nil {
		t.Fatal("expected error for unresolvable ref")
	}
	for _, frag := range []string{"macOS Keychain", "security unlock-keychain", "WENDY_SECRET_STORE=file", "wendy auth login"} {
		if !strings.Contains(err.Error(), frag) {
			t.Errorf("error %q missing %q", err.Error(), frag)
		}
	}
}

func TestAccessorUnknownRefVersion(t *testing.T) {
	useFakeStore(t, newFakeStore())
	c := CertificateInfo{PemPrivateKey: "keychain:v9:key-abc"}
	if _, err := c.PrivateKeyPEM(); err == nil {
		t.Fatal("expected error for unknown ref version")
	}
}

func TestAccountDerivationDeterministic(t *testing.T) {
	a1 := keyAccount("grpc.wendy.com:443", 7, "user-1")
	a2 := keyAccount("grpc.wendy.com:443", 7, "user-1")
	b := keyAccount("grpc.wendy.com:443", 8, "user-1")
	if a1 != a2 {
		t.Errorf("same inputs → different accounts: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Error("different org → same account")
	}
	if !strings.HasPrefix(a1, "key-") || len(a1) != len("key-")+16 {
		t.Errorf("account %q not key-<hex16>", a1)
	}
	tk := tokenAccount("grpc.wendy.com:443")
	if !strings.HasPrefix(tk, "token-") || len(tk) != len("token-")+16 {
		t.Errorf("token account %q not token-<hex16>", tk)
	}
}

func TestResolveErrorWhenNoBackend(t *testing.T) {
	origNew := newCredentialStore
	newCredentialStore = func() secretStoreIface { return nil }
	resetSecretCacheForTest()
	t.Cleanup(func() {
		newCredentialStore = origNew
		resetSecretCacheForTest()
	})
	c := CertificateInfo{PemPrivateKey: refPrefixV1 + "key-abc"}
	if _, err := c.PrivateKeyPEM(); err == nil {
		t.Fatal("expected error resolving a ref with no platform backend")
	}
}

var _ = errors.New // silence unused-import if errors ends up unused
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/config/ -run 'TestAccessor|TestAccount|TestResolve' -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

`go/internal/shared/config/secrets.go`:

```go
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
```

`go/internal/shared/config/secrets_darwin.go`:

```go
package config

import "github.com/wendylabsinc/wendy/go/internal/shared/secretstore"

// secretsPlatformDefault is true where the platform default is the
// Keychain. Variable (not const) so non-darwin CI can exercise the
// dehydration paths with a fake store.
var secretsPlatformDefault = true

func defaultCredentialStore() secretStoreIface {
	return secretstore.NewKeychain(credentialService)
}
```

`go/internal/shared/config/secrets_other.go`:

```go
//go:build !darwin

package config

// secretsPlatformDefault: no secret-store backend off darwin; secrets stay
// inline. Variable so tests can exercise dehydration with a fake store.
var secretsPlatformDefault = false

func defaultCredentialStore() secretStoreIface { return nil }
```

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/shared/config/ -count=1`
Expected: new tests PASS; pre-existing config tests untouched and green.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/config/
git commit -m "feat(config): keychain references + lazy memoized secret accessors

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `Save()` dehydration (migration), de-migration, never-lose-a-secret

**Files:**
- Modify: `go/internal/shared/config/config.go:184-201` (`Save`)
- Modify: `go/internal/shared/config/secrets.go` (add `dehydrate`, `inlineSecrets`, `hasInlineSecrets`, `clone`)
- Test: append to `go/internal/shared/config/secrets_test.go`

**Interfaces:**
- Consumes: Task 2's helpers.
- Produces: `func (c *Config) clone() *Config`; `func dehydrate(cfg *Config)`; `func inlineSecrets(cfg *Config)`; `func hasInlineSecrets(cfg *Config) bool`; `func dehydrateEnabled() bool`. `Save`'s signature is unchanged; the caller's `*Config` is never mutated (dehydration happens on a clone).

- [ ] **Step 1: Write the failing tests**

Append to `secrets_test.go` (uses `t.Setenv("HOME", t.TempDir())` so `Save`/`Load` hit a scratch config file):

```go
func TestSaveDehydratesAndLoadResolves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "")
	store := newFakeStore()
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	cfg := &Config{Auth: []AuthConfig{{
		CloudGRPC: "grpc.wendy.com:443",
		APIKey:    "tok-123",
		Certificates: []CertificateInfo{{
			PemCertificate: "CERT",
			PemPrivateKey:  "-----BEGIN PRIVATE KEY-----\nabc",
			OrganizationID: 7,
			UserID:         "user-1",
		}},
	}}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Caller's struct must be untouched (dehydration happens on a clone).
	if isRef(cfg.Auth[0].APIKey) || isRef(cfg.Auth[0].Certificates[0].PemPrivateKey) {
		t.Fatal("Save mutated the caller's config")
	}
	// On-disk JSON must contain no secret material.
	path, _ := configPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, secret := range []string{"tok-123", "BEGIN PRIVATE KEY"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("config.json still contains secret %q", secret)
		}
	}
	// Reload → fields are refs → accessors resolve.
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !isRef(loaded.Auth[0].APIKey) {
		t.Fatalf("APIKey on disk = %q, want a reference", loaded.Auth[0].APIKey)
	}
	tok, err := loaded.Auth[0].BearerToken()
	if err != nil || tok != "tok-123" {
		t.Fatalf("BearerToken = %q, %v", tok, err)
	}
	key, err := loaded.Auth[0].Certificates[0].PrivateKeyPEM()
	if err != nil || !strings.Contains(key, "BEGIN PRIVATE KEY") {
		t.Fatalf("PrivateKeyPEM = %q, %v", key, err)
	}
	// Certificates stayed inline (public material).
	if loaded.Auth[0].Certificates[0].PemCertificate != "CERT" {
		t.Error("public certificate was not left inline")
	}
}

func TestSavePutFailureKeepsSecretInline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "")
	store := newFakeStore()
	store.putErr = errors.New("keychain locked")
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "g", APIKey: "tok-123"}}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := Load()
	if loaded.Auth[0].APIKey != "tok-123" {
		t.Fatalf("APIKey = %q, want inline tok-123 after Put failure", loaded.Auth[0].APIKey)
	}
}

func TestSaveFileModeSkipsAndDeMigrates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newFakeStore()
	store.m["token-cafebabe0000dead"] = []byte("tok-999") // seeded ref target
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	t.Setenv("WENDY_SECRET_STORE", "file")
	cfg := &Config{Auth: []AuthConfig{{
		CloudGRPC: "g1",
		APIKey:    "tok-inline", // must STAY inline
	}, {
		CloudGRPC: "g2",
		APIKey:    refPrefixV1 + "token-cafebabe0000dead", // must be inlined back
	}}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := Load()
	if loaded.Auth[0].APIKey != "tok-inline" {
		t.Errorf("file mode dehydrated anyway: %q", loaded.Auth[0].APIKey)
	}
	if loaded.Auth[1].APIKey != "tok-999" {
		t.Errorf("file mode did not de-migrate ref: %q", loaded.Auth[1].APIKey)
	}
}

func TestSaveFileModeKeepsRefOnFailedRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakeStore(t, newFakeStore()) // empty store → ref unresolvable
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	t.Setenv("WENDY_SECRET_STORE", "file")
	ref := refPrefixV1 + "token-0123456789abcdef"
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "g", APIKey: ref}}}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, _ := Load()
	if loaded.Auth[0].APIKey != ref {
		t.Errorf("unresolvable ref was rewritten to %q; must keep the reference", loaded.Auth[0].APIKey)
	}
}

func TestHasInlineSecrets(t *testing.T) {
	if hasInlineSecrets(&Config{}) {
		t.Error("empty config reports inline secrets")
	}
	if hasInlineSecrets(&Config{Auth: []AuthConfig{{APIKey: refPrefixV1 + "token-x"}}}) {
		t.Error("all-refs config reports inline secrets")
	}
	if !hasInlineSecrets(&Config{Auth: []AuthConfig{{APIKey: "tok"}}}) {
		t.Error("inline APIKey not detected")
	}
	if !hasInlineSecrets(&Config{Auth: []AuthConfig{{Certificates: []CertificateInfo{{PemPrivateKey: "PEM"}}}}}) {
		t.Error("inline key not detected")
	}
}
```

Add imports `os` to the test file.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/config/ -run 'TestSave|TestHasInline' -v`
Expected: FAIL to build (`dehydrate`/`hasInlineSecrets` undefined).

- [ ] **Step 3: Implement**

Append to `secrets.go`:

```go
// dehydrateEnabled reports whether Save should move inline secrets into the
// platform store. WENDY_SECRET_STORE=file forces inline writes (and
// de-migration); everything else uses the platform default.
func dehydrateEnabled() bool {
	if os.Getenv("WENDY_SECRET_STORE") == "file" {
		return false
	}
	return secretsPlatformDefault
}

// clone deep-copies a Config via JSON round-trip so Save can rewrite secret
// fields without mutating the caller's struct.
func (c *Config) clone() *Config {
	data, err := json.Marshal(c)
	if err != nil {
		return c // marshaling plain structs cannot realistically fail; degrade to in-place
	}
	var out Config
	if err := json.Unmarshal(data, &out); err != nil {
		return c
	}
	return &out
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
			acct := tokenAccount(a.CloudGRPC)
			if store.Put(acct, []byte(a.APIKey)) == nil {
				cacheSecret(refPrefixV1+acct, a.APIKey)
				a.APIKey = refPrefixV1 + acct
			}
		}
		for j := range a.Certificates {
			c := &a.Certificates[j]
			if c.PemPrivateKey != "" && !isRef(c.PemPrivateKey) {
				acct := keyAccount(a.CloudGRPC, c.OrganizationID, c.UserID)
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
	for _, a := range cfg.Auth {
		if a.APIKey != "" && !isRef(a.APIKey) {
			return true
		}
		for _, c := range a.Certificates {
			if c.PemPrivateKey != "" && !isRef(c.PemPrivateKey) {
				return true
			}
		}
	}
	return false
}
```

(Add `"encoding/json"` and `"os"` to secrets.go imports.)

In `config.go`, `Save` becomes:

```go
// Save writes the configuration to ~/.wendy/config.json. On platforms with
// a credential store, inline secrets are moved into it and the file holds
// only references (see secrets.go); the caller's cfg is never mutated.
func Save(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}

	out := cfg.clone()
	if dehydrateEnabled() {
		dehydrate(out)
	} else {
		inlineSecrets(out)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	return nil
}
```

Note: `inlineSecrets` in the else-branch is correct for BOTH `file` mode (de-migration) and non-darwin (where refs only exist if a config was copied from a Mac — resolution fails there, refs are kept, behavior is unchanged for normal non-darwin configs which contain no refs).

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/shared/config/ -count=1`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/config/
git commit -m "feat(config): Save dehydrates secrets into the Keychain (auto-migration core)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Root-command migration hook + notice

**Files:**
- Modify: `go/internal/shared/config/secrets.go` (add `MigrateSecretsIfNeeded`)
- Modify: `go/internal/cli/commands/root.go:76-80` (call the hook in the synchronous zone, right after `maybeRefreshMCPSetup(cfg)`)
- Modify: `specs/2026-08-08-client-secrets-keychain-design.md` (§3: clarify that organic Saves migrate silently and the root hook owns the notice)
- Test: append to `go/internal/shared/config/secrets_test.go`

**Interfaces:**
- Consumes: Task 3's `hasInlineSecrets`, `dehydrateEnabled`, `Save`, `Load`.
- Produces: `func MigrateSecretsIfNeeded(cfg *Config) bool` — returns true when a migration ran and the reloaded config holds fewer inline secrets (the caller prints the notice).

- [ ] **Step 1: Write the failing tests**

```go
func TestMigrateSecretsIfNeeded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "")
	store := newFakeStore()
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "g", APIKey: "tok-123"}}}
	if err := Save(cfg); err != nil { // simulate a pre-existing config...
		t.Fatalf("seed Save: %v", err)
	}
	// ...that was written by an OLD cli: rewrite it inline via file mode.
	t.Setenv("WENDY_SECRET_STORE", "file")
	if err := Save(cfg); err != nil {
		t.Fatalf("inline Save: %v", err)
	}
	t.Setenv("WENDY_SECRET_STORE", "")

	loaded, _ := Load()
	if !hasInlineSecrets(loaded) {
		t.Fatal("test setup failed: config should hold inline secrets")
	}
	if !MigrateSecretsIfNeeded(loaded) {
		t.Fatal("MigrateSecretsIfNeeded = false, want true (migration ran)")
	}
	reloaded, _ := Load()
	if hasInlineSecrets(reloaded) {
		t.Error("config still holds inline secrets after migration")
	}
	// Second call: nothing left to migrate.
	if MigrateSecretsIfNeeded(reloaded) {
		t.Error("second MigrateSecretsIfNeeded = true, want false")
	}
}

func TestMigrateSecretsNoOpOffPlatformAndFileMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useFakeStore(t, newFakeStore())
	cfg := &Config{Auth: []AuthConfig{{CloudGRPC: "g", APIKey: "tok"}}}

	origDefault := secretsPlatformDefault
	secretsPlatformDefault = false // non-darwin
	if MigrateSecretsIfNeeded(cfg) {
		t.Error("migrated on a platform without a store")
	}
	secretsPlatformDefault = true
	t.Setenv("WENDY_SECRET_STORE", "file")
	if MigrateSecretsIfNeeded(cfg) {
		t.Error("migrated despite WENDY_SECRET_STORE=file")
	}
	secretsPlatformDefault = origDefault
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/config/ -run TestMigrateSecrets -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

Append to `secrets.go`:

```go
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
```

In `root.go`, directly after the `maybeRefreshMCPSetup(cfg)` call (synchronous zone, before the update-check goroutine that also saves cfg):

```go
		// Move plaintext credentials into the macOS Keychain (see
		// specs/2026-08-08-client-secrets-keychain-design.md). Runs in the
		// synchronous zone: the update-check goroutine below saves cfg too,
		// and its Save must observe an already-migrated on-disk state.
		if config.MigrateSecretsIfNeeded(cfg) {
			cmd.PrintErrln("Moved wendy credentials into the macOS Keychain (older wendy versions will need 'wendy auth login' again).")
		}
```

In the spec §3, replace the sentence
`the notice still prints only on the first migration (detected by "a field changed from inline to reference during this Save").`
with
`organic Saves migrate silently; the root hook owns the notice (a config fully migrated by an organic Save simply never triggers it).`

- [ ] **Step 4: Run tests + build**

Run: `go test ./go/internal/shared/config/ -count=1 && go build ./go/...`
Expected: PASS; whole tree builds.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/config/ go/internal/cli/commands/root.go specs/2026-08-08-client-secrets-keychain-design.md
git commit -m "feat(cli): auto-migrate plaintext credentials into the Keychain on startup

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Logout deletes Keychain items

**Files:**
- Modify: `go/internal/shared/config/secrets.go` (add `DeleteStoredSecrets`)
- Modify: `go/internal/cli/commands/auth.go:412-431` (logout command)
- Test: append to `go/internal/shared/config/secrets_test.go`

**Interfaces:**
- Consumes: Task 2's ref helpers and store seam.
- Produces: `func DeleteStoredSecrets(cfg *Config)` — best-effort deletion of every Keychain item referenced by the config, plus memoization-cache eviction.

- [ ] **Step 1: Write the failing test**

```go
func TestDeleteStoredSecrets(t *testing.T) {
	store := newFakeStore()
	store.m["token-aaaa"] = []byte("tok")
	store.m["key-bbbb"] = []byte("pem")
	useFakeStore(t, store)
	cfg := &Config{Auth: []AuthConfig{{
		APIKey: refPrefixV1 + "token-aaaa",
		Certificates: []CertificateInfo{
			{PemPrivateKey: refPrefixV1 + "key-bbbb"},
			{PemPrivateKey: "-----BEGIN PRIVATE KEY-----\ninline"}, // inline: nothing to delete
		},
	}}}
	DeleteStoredSecrets(cfg)
	if len(store.m) != 0 {
		t.Errorf("store still holds %d items after DeleteStoredSecrets", len(store.m))
	}
	if got := len(store.deletes); got != 2 {
		t.Errorf("deletes = %d, want 2 (refs only, not the inline value)", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./go/internal/shared/config/ -run TestDeleteStoredSecrets -v`
Expected: FAIL to build.

- [ ] **Step 3: Implement**

Append to `secrets.go`:

```go
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
```

In `auth.go`'s logout `RunE`, before `cfg.Auth = nil`:

```go
			// Discard the Keychain items the entries reference before
			// dropping the references themselves.
			config.DeleteStoredSecrets(cfg)
```

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/shared/config/ ./go/internal/cli/commands/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/config/ go/internal/cli/commands/auth.go
git commit -m "feat(auth): logout deletes Keychain-stored credentials

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Migrate all secret consumers to the accessors

**Files:**
- Modify (each listed site): see table below.
- Test: existing suites of every touched package (no new tests; behavior for inline values is identical by construction — the accessor returns the field verbatim).

**Interfaces:**
- Consumes: `PrivateKeyPEM()`, `BearerToken()`, `HasPrivateKey()`, `HasAPIKey()` (Task 2).
- Produces: no raw reads of `PemPrivateKey`/`APIKey` remain outside `go/internal/shared/config/` and the write sites in `auth.go`.

Three conversion patterns — every row below names one:

**P1 (pair-load):** `tls.X509KeyPair([]byte(x.PemCertificate), []byte(x.PemPrivateKey))` becomes
```go
keyPEM, err := x.PrivateKeyPEM()
if err != nil {
    return <zero>, fmt.Errorf("loading client key: %w", err)
}
cert, err := tls.X509KeyPair([]byte(x.PemCertificate), []byte(keyPEM))
```
(adapt the error return to the enclosing function's shape; never discard the accessor error).

**P2 (pass-as-arg):** `f(..., x.PemPrivateKey, ...)` becomes a preceding `keyPEM, err := x.PrivateKeyPEM()` + error propagation + `f(..., keyPEM, ...)`. Same for `auth.APIKey` → `BearerToken()`.

**P3 (presence check):** `x.PemPrivateKey == ""` / `!= ""` becomes `!x.HasPrivateKey()` / `x.HasPrivateKey()`; `auth.APIKey != ""` becomes `auth.HasAPIKey()`.

| Site | Pattern |
| --- | --- |
| `go/internal/cli/grpcclient/client.go:153-156` (`newAgentTLSConfig`) | P1 |
| `go/internal/cli/providers/microwendy.go:609` | P1 |
| `go/internal/cli/mcp/tools_cloud.go:424` | P1 |
| `go/internal/cli/mcp/tools_cloud.go:577`, `:603` | P2 |
| `go/internal/cli/mcp/tools_cloud.go:555-556` (`auth.APIKey` bearer) | P3 for the guard + P2 for the value |
| `go/internal/cli/commands/cloud_tunnel.go:120` | P1 |
| `go/internal/cli/commands/cloud_tunnel.go:233`, `:514` | P2 |
| `go/internal/cli/commands/cloud_tunnel.go:70-71` (`auth.APIKey`) | P3 + P2 |
| `go/internal/cli/commands/docker.go:1213` | P3 |
| `go/internal/cli/commands/docker.go:1238`, `:2201`, `:2229` | P2 |
| `go/internal/cli/commands/docker.go:2510` | P1 |
| `go/internal/cli/commands/os_provision.go:60` | P2 |
| `go/internal/cli/commands/os_install_linux_desktop.go:187` | P2 |
| `go/internal/cli/commands/device.go:802`, `:998` | P2 |
| `go/internal/cli/commands/helpers.go:1666` | P2 |
| `go/internal/cli/commands/auth.go:600` (refresh reads the existing key) | P2 |

Line numbers are as of branch base `58094509d` — locate by the surrounding expression if drifted. Write sites (`auth.go:269,391,644` key writes; `auth.go:277` APIKey write) stay raw field assignments — `Save()` converts.

- [ ] **Step 1: Convert every site in the table**

Apply the named pattern at each row. After the sweep, this guard must hold (run it):

```bash
grep -rn "\.PemPrivateKey" go/internal/cli go/cmd --include="*.go" | grep -v "_test.go" | grep -v "PrivateKeyPEM\|HasPrivateKey"
grep -rn "\.APIKey" go/internal/cli go/cmd --include="*.go" | grep -v "_test.go" | grep -v "BearerToken\|HasAPIKey"
```
Expected output: ONLY the write-site lines in `auth.go` (assignments/struct literals), nothing else.

- [ ] **Step 2: Run the touched packages' suites**

Run: `go test ./go/internal/cli/... -count=1` and `go build ./go/...`
Expected: everything green (inline values make the accessors identity functions, so no behavior change for existing tests).

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli go/cmd
git commit -m "refactor(cli): read client key and API token through secret accessors

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Login-shaped integration test

**Files:**
- Test: `go/internal/shared/config/secrets_integration_test.go`

**Interfaces:**
- Consumes: everything above; exercises the full `AddAuth → Save → Load → accessors` cycle exactly as `wendy auth login` does.

- [ ] **Step 1: Write the test**

```go
package config

import (
	"os"
	"strings"
	"testing"
)

// TestLoginFlowEndToEnd mirrors what `wendy auth login` does: build an
// AuthConfig with plaintext secrets, AddAuth, Save — then prove a fresh
// process (new Load, cold memoization cache) gets working credentials while
// the JSON on disk holds none.
func TestLoginFlowEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_SECRET_STORE", "")
	store := newFakeStore()
	useFakeStore(t, store)
	origDefault := secretsPlatformDefault
	secretsPlatformDefault = true
	t.Cleanup(func() { secretsPlatformDefault = origDefault })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.AddAuth(AuthConfig{
		CloudDashboard: "https://dash.wendy.com",
		CloudGRPC:      "grpc.wendy.com:443",
		APIKey:         "tok-login-e2e",
		Certificates: []CertificateInfo{{
			PemCertificate:      "-----BEGIN CERTIFICATE-----\npub",
			PemCertificateChain: "-----BEGIN CERTIFICATE-----\nchain",
			PemPrivateKey:       "-----BEGIN PRIVATE KEY-----\nsecret-key-e2e",
			OrganizationID:      7,
			UserID:              "user-e2e",
		}},
	})
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// "New process": cold cache, fresh Load.
	resetSecretCacheForTest()
	loaded, err := Load()
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	auth := loaded.Auth[0]
	tok, err := auth.BearerToken()
	if err != nil || tok != "tok-login-e2e" {
		t.Fatalf("BearerToken = %q, %v", tok, err)
	}
	key, err := auth.Certificates[0].PrivateKeyPEM()
	if err != nil || !strings.Contains(key, "secret-key-e2e") {
		t.Fatalf("PrivateKeyPEM = %q, %v", key, err)
	}
	if auth.Certificates[0].PemCertificate == "" || auth.Certificates[0].PemCertificateChain == "" {
		t.Error("public cert material missing after round-trip")
	}

	path, _ := configPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	for _, secret := range []string{"tok-login-e2e", "secret-key-e2e"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("config.json contains secret %q", secret)
		}
	}

	// Logout: items deleted.
	DeleteStoredSecrets(loaded)
	if len(store.m) != 0 {
		t.Errorf("store holds %d items after logout cleanup", len(store.m))
	}
}
```

- [ ] **Step 2: Run**

Run: `go test ./go/internal/shared/config/ -run TestLoginFlowEndToEnd -count=1 -v`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add go/internal/shared/config/secrets_integration_test.go
git commit -m "test(config): login-shaped end-to-end secret round-trip

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Full verification, push, stacked draft PR

**Files:** none new.

- [ ] **Step 1: gofmt + build + suites**

```bash
gofmt -l .
go build ./go/...
go test ./go/internal/shared/config/ ./go/internal/shared/secretstore/ ./go/internal/cli/tlscache/ ./go/internal/cli/grpcclient/ ./go/internal/cli/commands/ ./go/internal/cli/mcp/ ./go/internal/cli/providers/ -count=1
GOOS=linux go build ./go/...
```
Expected: gofmt empty; everything green; linux cross-build clean (non-darwin path compiles). Report any pre-existing failure unrelated to this branch instead of fixing it broadly.

- [ ] **Step 2: Push and open the stacked draft PR**

```bash
git push -u origin ed/client-key-keychain
gh pr create --draft --base ed/tls-session-resumption --title "Store client credentials in the macOS Keychain" --body "$(cat <<'EOF'
## Summary
`~/.wendy/config.json` held two plaintext secrets: the per-org ML-DSA client private key and the cloud API bearer token. On macOS both now live in the Keychain (service `wendy-credentials`); config.json holds `keychain:v1:<account>` references instead.

- **`go/internal/shared/secretstore`**: Keychain store + `security` runner promoted out of `tlscache` (which now consumes it; tickets keep their own service + env knob).
- **Lazy accessors** `PrivateKeyPEM()` / `BearerToken()` resolve references memoized (one `security` call per secret per process); commands that never touch a secret never touch the Keychain. Failures are actionable (`security unlock-keychain` / `WENDY_SECRET_STORE=file` guidance).
- **Migration = `Save()`**: inline secrets dehydrate on write; a root-hook migrates pre-existing configs once and prints a one-line notice. A failed Keychain write keeps the secret inline — a secret is never lost.
- **`WENDY_SECRET_STORE=file`**: inline-only writes; also the de-migration path (resolves refs back to inline values).
- **Logout** deletes the referenced Keychain items.
- **Non-macOS**: byte-identical behavior to today (inline plaintext) until a Secret-Service/DPAPI backend exists.

Stacked on #1612 (TLS session resumption) — reuses its Keychain plumbing. Spec: `specs/2026-08-08-client-secrets-keychain-design.md`.

## Compatibility
After migration, an OLDER wendy binary reading the config fails mTLS/cloud auth until `wendy auth login` is re-run (accepted trade-off; the migration notice says so).

## Verification
- [x] Unit + integration suites (fake `security` runner): dehydrate/resolve round-trip, memoization, Put-failure keeps inline, file-mode de-migration, logout cleanup, login-shaped e2e
- [ ] Real Mac: migrate a real config, promptless reads via the actual CLI binary, `wendy device info` works post-migration

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Update project memory** with the PR number and open verification gates.

---

## Self-Review Notes

- **Spec coverage:** §1 promotion → Task 1; §2 refs/accessors/memoization/error text → Task 2; §3 Save-migration + put-failure rule + de-migration + env semantics → Task 3, root hook + notice → Task 4 (including the spec-consistency edit); §4 lifecycle → Task 5 (logout) + Task 6 (write sites untouched); §5 testing → Tasks 2-7 + Task 8's real-Mac PR gate. Non-goals respected (no Linux/Windows backends; docker.go temp-key file mechanism untouched — only its key *source* changes in Task 6).
- **Known judgment calls encoded above:** `secretstore` under `internal/shared` (config must not import cli); `Store.Put` returns error (the never-lose-a-secret rule needs write confirmation); accessors on value receivers (call sites hold copies); `Save` clones before rewriting (54 callers keep their structs).
- **Type-consistency check:** `secretStoreIface = secretstore.Store` alias is what the test fakes implement; `newCredentialStore`/`secretsPlatformDefault` names match across Tasks 2-5; `refPrefixV1` used consistently.
