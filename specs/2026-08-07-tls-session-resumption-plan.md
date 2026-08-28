# TLS 1.3 Session Resumption Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Repeat `wendy` CLI connects to a provisioned agent resume TLS 1.3 sessions instead of redoing the ~2.2s ML-DSA mTLS handshake.

**Architecture:** A new `go/internal/cli/tlscache` package persists session tickets across CLI processes (macOS Keychain via `/usr/bin/security`; `0600` files elsewhere) and plugs into `ConnectWithTLSAndPins` as a `tls.ClientSessionCache`. The agent's `mtls.NewTLSConfig` gains `WrapSession`/`UnwrapSession` that stamp the verified client cert's validity window into the ticket and *decline* (never fail) resumption when the window has lapsed. Spec: `specs/2026-08-07-tls-session-resumption-design.md`.

**Tech Stack:** Go 1.26 `crypto/tls` session APIs (`ClientSessionState.ResumptionState`, `tls.NewResumptionState`, `SessionState.Bytes`, `tls.ParseSessionState`, `Config.EncryptTicket`/`DecryptTicket`, `WrapSession`/`UnwrapSession`), grpc-go, macOS `security` CLI.

## Global Constraints

- Module root is the **repo root** (`go.mod`: `module github.com/wendylabsinc/wendy`, `go 1.26.5`); packages live under `go/...`. Run all `go` commands from the repo root.
- Repo-root `docs/` is a **symlink into `go/internal/cli/assets/docs`** (embedded CLI assets) — never write docs there; specs/plans go in top-level `specs/`.
- Cache/store failures must NEVER surface as errors — every failure path is "no session → full handshake".
- The server must NEVER fail a handshake because of a bad/stale ticket — it declines resumption (`UnwrapSession` returns `nil, nil`) and continues with a full handshake.
- Env override: `WENDY_TLS_SESSION_STORE=keychain|file|off` (exact values; `off` disables caching; unknown values = platform default).
- Keychain writes must not put the secret on argv (visible in `ps`) — use `security -i` with the command on stdin.
- All tests: standard `go test` (no build tags needed except darwin-only files); before pushing run `gofmt -l .` from repo root and `gofmt -w` anything listed.
- Commit messages end with: `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`

---

### Task 1: `sessionStore` interface + file backend

**Files:**
- Create: `go/internal/cli/tlscache/store.go`
- Create: `go/internal/cli/tlscache/store_file.go`
- Test: `go/internal/cli/tlscache/store_file_test.go`

**Interfaces:**
- Consumes: `config.ConfigDir()` from `go/internal/shared/config` (returns `~/.wendy`, creating it).
- Produces: `type sessionStore interface { get(key string) []byte; put(key string, blob []byte); delete(key string) }`; `newFileStore() sessionStore` (nil if no home dir); `type fileStore struct{ dir string }` (tests construct it directly with a temp dir); `const sessionFileMaxAge = 7 * 24 * time.Hour`. Task 2 adds `newDefaultStore()`/`newKeychainStore()`/`newPlatformStore()`; Task 3 consumes `sessionStore`.

- [ ] **Step 1: Write the failing tests**

`go/internal/cli/tlscache/store_file_test.go`:

```go
package tlscache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := &fileStore{dir: filepath.Join(t.TempDir(), "tls-sessions")}
	if got := s.get("k1"); got != nil {
		t.Fatalf("get on empty store = %q, want nil", got)
	}
	s.put("k1", []byte("blob-1"))
	if got := string(s.get("k1")); got != "blob-1" {
		t.Fatalf("get after put = %q, want blob-1", got)
	}
	s.put("k1", []byte("blob-2")) // overwrite
	if got := string(s.get("k1")); got != "blob-2" {
		t.Fatalf("get after overwrite = %q, want blob-2", got)
	}
	s.delete("k1")
	if got := s.get("k1"); got != nil {
		t.Fatalf("get after delete = %q, want nil", got)
	}
	s.delete("k1") // deleting a missing key must not panic
}

func TestFileStorePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	s.put("k1", []byte("secret"))

	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir perm = %o, want 700", perm)
	}
	fi, err := os.Stat(filepath.Join(dir, "k1.tlssession"))
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

func TestFileStorePrunesOldSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	s.put("old", []byte("stale"))
	oldPath := filepath.Join(dir, "old.tlssession")
	past := time.Now().Add(-sessionFileMaxAge - time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	s.put("fresh", []byte("new")) // put triggers pruning
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("expected old session pruned, stat err = %v", err)
	}
	if got := string(s.get("fresh")); got != "new" {
		t.Errorf("fresh session lost by pruning: %q", got)
	}
}

func TestFileStoreNoTempFileLeftovers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls-sessions")
	s := &fileStore{dir: dir}
	s.put("k1", []byte("blob"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "k1.tlssession" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir contents = %v, want exactly [k1.tlssession]", names)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/cli/tlscache/ -run TestFileStore -v`
Expected: FAIL to build — `fileStore` undefined.

- [ ] **Step 3: Write the implementation**

`go/internal/cli/tlscache/store.go`:

```go
// Package tlscache persists TLS 1.3 session tickets across CLI invocations so
// repeat connects to a provisioned agent resume the session instead of redoing
// the full ML-DSA mTLS handshake (~2.2s on device hardware; see
// specs/2026-08-07-tls-session-resumption-design.md).
package tlscache

// A sessionStore persists opaque session blobs by key. Implementations treat
// every failure as a cache miss and never return errors: resumption is an
// optimization whose universal fallback is a full handshake.
type sessionStore interface {
	get(key string) []byte // nil on miss or any error
	put(key string, blob []byte)
	delete(key string)
}
```

`go/internal/cli/tlscache/store_file.go`:

```go
package tlscache

import (
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// sessionFileMaxAge matches crypto/tls's maxSessionTicketLifetime: a ticket
// older than 7 days can never resume, so its file is dead weight.
const sessionFileMaxAge = 7 * 24 * time.Hour

// fileStore keeps one 0600 file per session under a 0700 directory. It is the
// default backend on platforms without a secret store; the client's ML-DSA
// private key already lives unencrypted in ~/.wendy/config.json there, so a
// 0600 ticket file adds no new exposure class.
type fileStore struct{ dir string }

func newFileStore() sessionStore {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil
	}
	return &fileStore{dir: filepath.Join(dir, "tls-sessions")}
}

func (s *fileStore) path(key string) string {
	return filepath.Join(s.dir, key+".tlssession")
}

func (s *fileStore) get(key string) []byte {
	blob, err := os.ReadFile(s.path(key))
	if err != nil {
		return nil
	}
	return blob
}

func (s *fileStore) put(key string, blob []byte) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	// Atomic replace: concurrent CLI processes are last-writer-wins, and a
	// reader never observes a partial file.
	tmp, err := os.CreateTemp(s.dir, key+".tmp*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(blob); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return
	}
	if err := os.Rename(tmpName, s.path(key)); err != nil {
		os.Remove(tmpName)
		return
	}
	s.prune()
}

func (s *fileStore) delete(key string) {
	os.Remove(s.path(key))
}

// prune drops session files old enough that their tickets can no longer
// resume. Best-effort by design.
func (s *fileStore) prune() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-sessionFileMaxAge)
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".tlssession" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/cli/tlscache/ -run TestFileStore -v`
Expected: 4 PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/tlscache/
git commit -m "feat(tlscache): sessionStore interface + 0600 file backend

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Keychain backend (darwin) + backend selection

**Files:**
- Create: `go/internal/cli/tlscache/store_keychain_darwin.go`
- Create: `go/internal/cli/tlscache/store_select_darwin.go`
- Create: `go/internal/cli/tlscache/store_select_other.go`
- Modify: `go/internal/cli/tlscache/store.go` (add `newDefaultStore`)
- Test: `go/internal/cli/tlscache/store_keychain_darwin_test.go`
- Test: `go/internal/cli/tlscache/store_select_test.go`

**Interfaces:**
- Consumes: `sessionStore`, `newFileStore()` from Task 1.
- Produces: `newDefaultStore() sessionStore` (nil = caching disabled) honoring `WENDY_TLS_SESSION_STORE=keychain|file|off`; darwin-only `newKeychainStore() sessionStore` and swappable `var runSecurity func(ctx context.Context, stdin string, args ...string) ([]byte, error)`. Task 3 consumes `newDefaultStore`.

- [ ] **Step 1: Write the failing tests**

`go/internal/cli/tlscache/store_select_test.go` (all platforms):

```go
package tlscache

import "testing"

func TestNewDefaultStoreEnvOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep newFileStore off the real ~/.wendy

	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	if s := newDefaultStore(); s != nil {
		t.Errorf("store=off: got %T, want nil", s)
	}

	t.Setenv("WENDY_TLS_SESSION_STORE", "file")
	if _, ok := newDefaultStore().(*fileStore); !ok {
		t.Errorf("store=file: got %T, want *fileStore", newDefaultStore())
	}

	t.Setenv("WENDY_TLS_SESSION_STORE", "bogus")
	if s := newDefaultStore(); s == nil {
		t.Error("store=bogus: got nil, want platform default store")
	}
}
```

`go/internal/cli/tlscache/store_keychain_darwin_test.go`:

```go
package tlscache

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeSecurity records invocations of the security CLI and returns canned output.
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

func TestKeychainGetDecodesBase64(t *testing.T) {
	blob := []byte("session-blob")
	fake := &fakeSecurity{out: []byte(base64.StdEncoding.EncodeToString(blob) + "\n")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()

	got := newKeychainStore().get("abc123")
	if string(got) != "session-blob" {
		t.Fatalf("get = %q, want session-blob", got)
	}
	args := fake.calls[0].args
	want := []string{"find-generic-password", "-s", "wendy-tls-session", "-a", "abc123", "-w"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", args, want)
	}
}

func TestKeychainGetMissOrDenied(t *testing.T) {
	fake := &fakeSecurity{err: errors.New("exit status 44")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	if got := newKeychainStore().get("abc123"); got != nil {
		t.Errorf("get on security error = %q, want nil", got)
	}
}

func TestKeychainGetBadBase64(t *testing.T) {
	fake := &fakeSecurity{out: []byte("!!! not base64 !!!")}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	if got := newKeychainStore().get("abc123"); got != nil {
		t.Errorf("get on undecodable payload = %q, want nil", got)
	}
}

func TestKeychainPutKeepsSecretOffArgv(t *testing.T) {
	fake := &fakeSecurity{}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()

	blob := []byte("ticket-secret")
	newKeychainStore().put("abc123", blob)

	call := fake.calls[0]
	// The whole add command rides stdin via `security -i`; argv holds only the flag.
	if strings.Join(call.args, " ") != "-i" {
		t.Fatalf("argv = %v, want [-i]", call.args)
	}
	b64 := base64.StdEncoding.EncodeToString(blob)
	for _, frag := range []string{"add-generic-password", "-U", "-s wendy-tls-session", "-a abc123", "-w " + b64} {
		if !strings.Contains(call.stdin, frag) {
			t.Errorf("stdin %q missing %q", call.stdin, frag)
		}
	}
}

func TestKeychainDelete(t *testing.T) {
	fake := &fakeSecurity{}
	orig := runSecurity
	runSecurity = fake.run
	defer func() { runSecurity = orig }()
	newKeychainStore().delete("abc123")
	want := []string{"delete-generic-password", "-s", "wendy-tls-session", "-a", "abc123"}
	if strings.Join(fake.calls[0].args, " ") != strings.Join(want, " ") {
		t.Errorf("args = %v, want %v", fake.calls[0].args, want)
	}
}

func TestDarwinDefaultIsKeychain(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "")
	if _, ok := newDefaultStore().(keychainStore); !ok {
		t.Errorf("darwin default = %T, want keychainStore", newDefaultStore())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/cli/tlscache/ -run 'TestKeychain|TestNewDefaultStore|TestDarwinDefault' -v`
Expected: FAIL to build — `runSecurity`, `newKeychainStore`, `newDefaultStore` undefined.

- [ ] **Step 3: Write the implementation**

Append to `go/internal/cli/tlscache/store.go`:

```go
import "os"

// newDefaultStore picks the ticket store backend. WENDY_TLS_SESSION_STORE
// forces one: "off" disables caching (right for CI), "file"/"keychain" force a
// backend. Anything else gets the platform default (Keychain on macOS, files
// elsewhere). A nil return disables session caching entirely.
func newDefaultStore() sessionStore {
	switch os.Getenv("WENDY_TLS_SESSION_STORE") {
	case "off":
		return nil
	case "file":
		return newFileStore()
	case "keychain":
		return newKeychainStore()
	}
	return newPlatformStore()
}
```

`go/internal/cli/tlscache/store_select_darwin.go`:

```go
package tlscache

func newPlatformStore() sessionStore { return newKeychainStore() }
```

`go/internal/cli/tlscache/store_select_other.go`:

```go
//go:build !darwin

package tlscache

func newPlatformStore() sessionStore { return newFileStore() }

// newKeychainStore has no non-darwin implementation; an explicit
// WENDY_TLS_SESSION_STORE=keychain falls back to files rather than failing.
func newKeychainStore() sessionStore { return newFileStore() }
```

`go/internal/cli/tlscache/store_keychain_darwin.go`:

```go
package tlscache

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// keychainService names the Keychain items holding wendy session tickets.
const keychainService = "wendy-tls-session"

const securityTimeout = 5 * time.Second

// runSecurity invokes /usr/bin/security (same pattern as
// wifi_scan_darwin.go's lookupKeychainPassword). Swapped in tests.
var runSecurity = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	return cmd.Output()
}

// keychainStore keeps session tickets in the user's login Keychain. A ticket
// is a bearer resumption secret, so it goes in the platform secret store
// rather than a file. Items are created by (and read back through)
// /usr/bin/security itself, whose default ACL covers it — reads must never
// prompt; any prompt/denial surfaces as a plain cache miss.
type keychainStore struct{}

func newKeychainStore() sessionStore { return keychainStore{} }

func (keychainStore) get(key string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	out, err := runSecurity(ctx, "", "find-generic-password", "-s", keychainService, "-a", key, "-w")
	if err != nil {
		return nil // not found, denied, or security failed — cache miss
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil
	}
	return blob
}

func (keychainStore) put(key string, blob []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	// `security -i` reads the command from stdin so the ticket secret never
	// appears on argv (argv is world-readable via ps). base64 and the hex key
	// contain no whitespace, so no quoting is needed.
	cmdLine := fmt.Sprintf("add-generic-password -U -s %s -a %s -j wendy-cli-tls-session -w %s\n",
		keychainService, key, base64.StdEncoding.EncodeToString(blob))
	_, _ = runSecurity(ctx, cmdLine, "-i")
}

func (keychainStore) delete(key string) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()
	_, _ = runSecurity(ctx, "", "delete-generic-password", "-s", keychainService, "-a", key)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/cli/tlscache/ -v`
Expected: all PASS (file + keychain + selection).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/tlscache/
git commit -m "feat(tlscache): macOS Keychain backend + WENDY_TLS_SESSION_STORE selection

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `Cache` — the `tls.ClientSessionCache` implementation

**Files:**
- Create: `go/internal/cli/tlscache/cache.go`
- Test: `go/internal/cli/tlscache/cache_test.go`

**Interfaces:**
- Consumes: `sessionStore`, `newDefaultStore()`.
- Produces: `func ForTarget(address string, clientLeafDER []byte) *Cache` (nil when caching disabled); `(*Cache) Get(string) (*tls.ClientSessionState, bool)`; `(*Cache) Put(string, *tls.ClientSessionState)`; `(*Cache) Flush()` (waits for async persists; used by tests and available to callers at shutdown). Task 4 consumes `ForTarget`.

- [ ] **Step 1: Write the failing tests**

`go/internal/cli/tlscache/cache_test.go` — the round-trip test uses a real loopback TLS 1.3 handshake because `tls.ClientSessionState` cannot be fabricated:

```go
package tlscache

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory sessionStore recording deletes.
type memStore struct {
	mu      sync.Mutex
	m       map[string][]byte
	deletes int
}

func newMemStore() *memStore { return &memStore{m: map[string][]byte{}} }

func (s *memStore) get(key string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[key]
}
func (s *memStore) put(key string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = blob
}
func (s *memStore) delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	s.deletes++
}

// startTLSServer runs a minimal TLS 1.3 server issuing session tickets;
// each accepted conn handshakes, reports DidResume on ch, writes one byte.
func startTLSServer(t *testing.T) (addr string, ch <-chan bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "tlscache-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS13,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	resumed := make(chan bool, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc := tls.Server(c, cfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				resumed <- tc.ConnectionState().DidResume
				tc.Write([]byte{1}) // flushes the session ticket to the client
				buf := make([]byte, 1)
				tc.Read(buf) // wait for client close so the ticket is processed
			}(c)
		}
	}()
	return ln.Addr().String(), resumed
}

func dialWithCache(t *testing.T, addr string, cache *Cache) bool {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		ClientSessionCache: cache,
		MinVersion:         tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil { // ensures NewSessionTicket has arrived
		t.Fatalf("read: %v", err)
	}
	return conn.ConnectionState().DidResume
}

func TestCacheRoundTripAcrossInstances(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()
	leafDER := []byte("client-leaf-der") // identity input only; any bytes work here

	c1 := newCache("cache-test", leafDER, store)
	if resumed := dialWithCache(t, addr, c1); resumed {
		t.Fatal("first connection unexpectedly resumed")
	}
	<-srvResumed
	c1.Flush() // wait for the async persist

	// A separate Cache instance over the same store simulates a new CLI process.
	c2 := newCache("cache-test", leafDER, store)
	if resumed := dialWithCache(t, addr, c2); !resumed {
		t.Fatal("second connection did not resume from persisted session")
	}
	if got := <-srvResumed; !got {
		t.Fatal("server did not observe resumption")
	}
}

func TestCacheKeyedByClientCert(t *testing.T) {
	addr, srvResumed := startTLSServer(t)
	store := newMemStore()

	c1 := newCache("cache-test", []byte("cert-A"), store)
	dialWithCache(t, addr, c1)
	<-srvResumed
	c1.Flush()

	// Same target, different client cert → different store key → no resumption.
	c2 := newCache("cache-test", []byte("cert-B"), store)
	if resumed := dialWithCache(t, addr, c2); resumed {
		t.Fatal("session resumed across different client certs")
	}
	<-srvResumed
}

func TestCacheCorruptBlobIsMissAndDeleted(t *testing.T) {
	store := newMemStore()
	c := newCache("cache-test", []byte("cert"), store)
	store.put(c.storeKey, []byte("WTS1garbage-not-a-session"))
	if _, ok := c.Get("ignored"); ok {
		t.Fatal("corrupt blob returned a session")
	}
	if store.deletes == 0 {
		t.Error("corrupt blob was not deleted")
	}
}

func TestCachePutNilEvicts(t *testing.T) {
	store := newMemStore()
	c := newCache("cache-test", []byte("cert"), store)
	store.put(c.storeKey, []byte("whatever"))
	c.Put("ignored", nil)
	c.Flush()
	if store.get(c.storeKey) != nil {
		t.Error("Put(nil) did not evict the stored session")
	}
}

func TestForTargetOffReturnsNil(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	if c := ForTarget("host:50052", []byte("cert")); c != nil {
		t.Errorf("ForTarget with store=off = %v, want nil", c)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/cli/tlscache/ -run TestCache -v`
Expected: FAIL to build — `Cache`, `newCache`, `ForTarget` undefined.

- [ ] **Step 3: Write the implementation**

`go/internal/cli/tlscache/cache.go`:

```go
package tlscache

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"sync"
)

// blobMagic versions the on-disk/on-keychain blob layout:
// "WTS1" | uint32 BE ticket length | ticket | SessionState.Bytes().
const blobMagic = "WTS1"

// Cache implements tls.ClientSessionCache for a single (target address, client
// certificate) pair, persisting the most recent session via a sessionStore.
//
// The store key binds the client leaf certificate on purpose: a session ticket
// embeds the client identity verified at the original handshake, so a ticket
// obtained with one org's cert must never be offered when dialing with
// another's. Go's own sessionKey (the remote address) is ignored.
type Cache struct {
	storeKey string
	store    sessionStore
	wg       sync.WaitGroup
}

// ForTarget returns a Cache bound to the target address and client leaf cert
// (DER), or nil when session caching is disabled (WENDY_TLS_SESSION_STORE=off
// or no usable backend). Callers must skip the tls.Config wiring on nil — a
// nil *Cache inside a non-nil interface value would panic in crypto/tls.
func ForTarget(address string, clientLeafDER []byte) *Cache {
	store := newDefaultStore()
	if store == nil {
		return nil
	}
	return newCache(address, clientLeafDER, store)
}

func newCache(address string, clientLeafDER []byte, store sessionStore) *Cache {
	certSum := sha256.Sum256(clientLeafDER)
	sum := sha256.Sum256(append([]byte(address+"|"), certSum[:]...))
	return &Cache{storeKey: hex.EncodeToString(sum[:]), store: store}
}

// Get implements tls.ClientSessionCache. Any decode failure evicts the entry
// and reports a miss; the caller then performs a full handshake.
func (c *Cache) Get(string) (*tls.ClientSessionState, bool) {
	blob := c.store.get(c.storeKey)
	if blob == nil {
		return nil, false
	}
	ticket, stateBytes, ok := decodeSessionBlob(blob)
	if !ok {
		c.store.delete(c.storeKey)
		return nil, false
	}
	state, err := tls.ParseSessionState(stateBytes)
	if err != nil {
		c.store.delete(c.storeKey)
		return nil, false
	}
	cs, err := tls.NewResumptionState(ticket, state)
	if err != nil {
		c.store.delete(c.storeKey)
		return nil, false
	}
	return cs, true
}

// Put implements tls.ClientSessionCache. crypto/tls calls it from the
// connection's record-processing path, so persistence (a Keychain subprocess
// on macOS) happens on a background goroutine; a ticket lost to process exit
// just means a full handshake next time. Put(nil) evicts (crypto/tls uses
// that on certain handshake failures).
func (c *Cache) Put(_ string, cs *tls.ClientSessionState) {
	if cs == nil {
		c.async(func() { c.store.delete(c.storeKey) })
		return
	}
	ticket, state, err := cs.ResumptionState()
	if err != nil || state == nil {
		return
	}
	stateBytes, err := state.Bytes()
	if err != nil {
		return
	}
	blob := encodeSessionBlob(ticket, stateBytes)
	c.async(func() { c.store.put(c.storeKey, blob) })
}

// Flush blocks until all pending async persists complete. Tests rely on it;
// callers may use it before exit to make best-effort persistence certain.
func (c *Cache) Flush() { c.wg.Wait() }

func (c *Cache) async(fn func()) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		fn()
	}()
}

func encodeSessionBlob(ticket, state []byte) []byte {
	blob := make([]byte, 0, len(blobMagic)+4+len(ticket)+len(state))
	blob = append(blob, blobMagic...)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ticket)))
	blob = append(blob, l[:]...)
	blob = append(blob, ticket...)
	blob = append(blob, state...)
	return blob
}

func decodeSessionBlob(blob []byte) (ticket, state []byte, ok bool) {
	header := len(blobMagic) + 4
	if len(blob) < header || string(blob[:len(blobMagic)]) != blobMagic {
		return nil, nil, false
	}
	n := binary.BigEndian.Uint32(blob[len(blobMagic):header])
	if uint32(len(blob)-header) < n {
		return nil, nil, false
	}
	return blob[header : header+int(n)], blob[header+int(n):], true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/cli/tlscache/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/tlscache/
git commit -m "feat(tlscache): persistent ClientSessionCache keyed by target + client cert

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Wire the cache into `ConnectWithTLSAndPins` + WENDY_TLS_DEBUG logging

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go:138-195` (`ConnectWithTLSAndPins`)
- Test: `go/internal/cli/grpcclient/tls_config_test.go`

**Interfaces:**
- Consumes: `tlscache.ForTarget(address string, clientLeafDER []byte) *Cache`.
- Produces: unexported `newAgentTLSConfig(address string, certInfo *config.CertificateInfo, pins certs.PinChecker, observedOrg *atomic.Int32) (*tls.Config, error)` — extracted from `ConnectWithTLSAndPins` so the config is testable; `var tlsDebugWriter io.Writer = os.Stderr` for test capture. Task 8's integration test drives `ConnectWithTLSAndPins` end-to-end.

- [ ] **Step 1: Write the failing tests**

`go/internal/cli/grpcclient/tls_config_test.go` (package `grpcclient`); imports: `bytes`, `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/tls`, `crypto/x509`, `crypto/x509/pkix`, `encoding/pem`, `math/big`, `strings`, `sync/atomic`, `testing`, `time`, and the repo's `internal/shared/config`:

```go
// testCertInfo builds a self-contained ECDSA CA + client leaf and returns the
// CertificateInfo shape ConnectWithTLSAndPins consumes.
func testCertInfo(t *testing.T) *config.CertificateInfo {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TLS Config Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "tls-config-test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return &config.CertificateInfo{
		PemCertificate:      string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
		PemPrivateKey:       string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		PemCertificateChain: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
	}
}
func TestNewAgentTLSConfigSetsSessionCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_TLS_SESSION_STORE", "file")
	cfg, err := newAgentTLSConfig("192.168.1.10:50052", testCertInfo(t), nil, new(atomic.Int32))
	if err != nil {
		t.Fatalf("newAgentTLSConfig: %v", err)
	}
	if cfg.ClientSessionCache == nil {
		t.Error("ClientSessionCache not set")
	}
}

func TestNewAgentTLSConfigHonorsStoreOff(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	cfg, err := newAgentTLSConfig("192.168.1.10:50052", testCertInfo(t), nil, new(atomic.Int32))
	if err != nil {
		t.Fatalf("newAgentTLSConfig: %v", err)
	}
	if cfg.ClientSessionCache != nil {
		t.Errorf("ClientSessionCache = %v, want nil with store=off", cfg.ClientSessionCache)
	}
}

func TestNewAgentTLSConfigDebugLogsResumption(t *testing.T) {
	t.Setenv("WENDY_TLS_SESSION_STORE", "off")
	t.Setenv("WENDY_TLS_DEBUG", "1")
	var buf bytes.Buffer
	origWriter := tlsDebugWriter
	tlsDebugWriter = &buf
	defer func() { tlsDebugWriter = origWriter }()

	cfg, err := newAgentTLSConfig("192.168.1.10:50052", testCertInfo(t), nil, new(atomic.Int32))
	if err != nil {
		t.Fatalf("newAgentTLSConfig: %v", err)
	}
	// The wrapped VerifyConnection must log DidResume before delegating; an
	// empty ConnectionState fails the inner verifier, which is fine here.
	cfg.VerifyConnection(tls.ConnectionState{DidResume: true})
	if !strings.Contains(buf.String(), "resumed=true") {
		t.Errorf("debug output %q missing resumed=true", buf.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/cli/grpcclient/ -run TestNewAgentTLSConfig -v`
Expected: FAIL to build — `newAgentTLSConfig`, `tlsDebugWriter` undefined.

- [ ] **Step 3: Implement**

In `go/internal/cli/grpcclient/client.go`, add `var tlsDebugWriter io.Writer = os.Stderr` at package level (imports: `io`, `os`), then extract the existing tlsCfg construction from `ConnectWithTLSAndPins` (the `tls.X509KeyPair` call through the `tlsCfg := &tls.Config{...}` literal) into:

```go
// newAgentTLSConfig builds the client TLS config for one agent target,
// including the persistent session cache that lets repeat CLI invocations
// skip the full ML-DSA handshake (see specs/2026-08-07-tls-session-resumption-design.md).
func newAgentTLSConfig(address string, certInfo *config.CertificateInfo, pins certs.PinChecker, observedOrg *atomic.Int32) (*tls.Config, error) {
	// Only load the leaf cert — not the chain. Go's TLS library calls
	// x509.ParseCertificate on every cert sent in the handshake, and ML-DSA
	// chain certs (from pki-core) cause parse failures on the agent's server.
	// The agent's VerifyPeerCertificate callback verifies the client cert via
	// its own ML-DSA-aware CA pool without needing the chain in the handshake.
	cert, err := tls.X509KeyPair(
		[]byte(certInfo.PemCertificate),
		[]byte(certInfo.PemPrivateKey),
	)
	if err != nil {
		return nil, fmt.Errorf("loading TLS cert: %w", err)
	}
	verifyConn, err := certs.BuildServerVerifyConnection(certs.ServerVerifyOpts{
		ChainPEM:      certInfo.PemCertificateChain,
		ExpectedOrgID: int32(certInfo.OrganizationID),
		PinStore:      pins,
		OnServerIdentity: func(id certs.WendyIdentity) {
			if id.OrgID != 0 {
				observedOrg.Store(id.OrgID)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("building TLS verifier: %w", err)
	}
	if os.Getenv("WENDY_TLS_DEBUG") != "" {
		inner := verifyConn
		verifyConn = func(cs tls.ConnectionState) error {
			fmt.Fprintf(tlsDebugWriter, "[tls-debug] %s resumed=%v\n", address, cs.DidResume)
			return inner(cs)
		}
	}
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
		VerifyConnection:   verifyConn,
		MinVersion:         tls.VersionTLS12,
	}
	// Session resumption: nil means caching is disabled — leaving the field
	// unset is required then, because a typed-nil *Cache in the interface
	// would panic inside crypto/tls.
	if cache := tlscache.ForTarget(address, cert.Certificate[0]); cache != nil {
		tlsCfg.ClientSessionCache = cache
	}
	return tlsCfg, nil
}
```

`ConnectWithTLSAndPins` becomes: create `observedOrg := new(atomic.Int32)`, call `newAgentTLSConfig(address, certInfo, pins, observedOrg)`, keep everything from `grpc.NewClient` onward unchanged. Import `"github.com/wendylabsinc/wendy/go/internal/cli/tlscache"`.

- [ ] **Step 4: Run tests, including the untouched existing ones**

Run: `go test ./go/internal/cli/grpcclient/ -v`
Expected: all PASS (new + pre-existing).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/grpcclient/
git commit -m "feat(grpcclient): persistent TLS session cache + resumed= debug logging

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Extract `effectiveVerificationTime` (behavior-preserving refactor)

**Files:**
- Modify: `go/internal/agent/mtls/mldsa_verify.go:180-195` (inside `buildVerifyPeerCertificate`)

**Interfaces:**
- Produces: `func effectiveVerificationTime(realNow, notBeforeFloor, certNotBefore time.Time) time.Time` in package `mtls`. Task 6 consumes it so the ticket re-check can never drift from the full verifier's clock-skew semantics.

- [ ] **Step 1: Extract the helper**

In `mldsa_verify.go`, replace the body between `realNow := time.Now()` and the expiry pre-check with a call to the new helper, moving the existing comments with the code:

```go
// effectiveVerificationTime returns the clock used for NotBefore checks.
// effectiveNow applies the NotBefore floor; when the device clock is behind
// notBeforeFloor (NTP not yet synced), it additionally advances up to the
// cert's NotBefore so a cert issued just after provisioning is not spuriously
// rejected — capped at notBeforeFloor+maxClockSkewTolerance so a cert whose
// NotBefore is further in the future is still rejected on a stuck clock.
// Shared by the full ML-DSA verifier and the session-ticket re-check
// (session_ticket.go) so the two can never drift apart.
func effectiveVerificationTime(realNow, notBeforeFloor, certNotBefore time.Time) time.Time {
	effectiveNow := maxTime(realNow, notBeforeFloor)
	if realNow.Before(notBeforeFloor) {
		advanced := certNotBefore
		if cap := notBeforeFloor.Add(maxClockSkewTolerance); advanced.After(cap) {
			advanced = cap
		}
		effectiveNow = maxTime(effectiveNow, advanced)
	}
	return effectiveNow
}
```

and in `buildVerifyPeerCertificate`:

```go
	realNow := time.Now()
	effectiveNow := effectiveVerificationTime(realNow, notBeforeFloor, leaf.NotBefore)
```

- [ ] **Step 2: Run the existing floor tests as the refactor guard**

Run: `go test ./go/internal/agent/mtls/ -v`
Expected: all PASS unchanged (`TestBuildVerifyPeerCertificate_StandardPathHonorsNotBeforeFloor`, `TestBuildVerifyPeerCertificate_ClientCertIssuedAfterFloor`, and the rest).

- [ ] **Step 3: Commit**

```bash
git add go/internal/agent/mtls/mldsa_verify.go
git commit -m "refactor(mtls): extract effectiveVerificationTime for reuse by ticket checks

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Server `WrapSession`/`UnwrapSession` with cert-window decline

**Files:**
- Create: `go/internal/agent/mtls/session_ticket.go`
- Modify: `go/internal/agent/mtls/server.go:57-72` (`NewTLSConfig` return)
- Test: `go/internal/agent/mtls/session_ticket_test.go`

**Interfaces:**
- Consumes: `effectiveVerificationTime` (Task 5), `maxClockSkewTolerance`.
- Produces: `func wireSessionTicketChecks(cfg *tls.Config, notBeforeFloor time.Time, now func() time.Time)`; `const ticketMetaPrefix = "wendy-mtls/1:"`; pure helpers `appendClientCertWindow(ss *tls.SessionState, cs tls.ConnectionState)`, `clientCertWindowFromExtra(ss *tls.SessionState) (notBefore, notAfter time.Time, ok bool)`, `resumableClientWindow(notBefore, notAfter, realNow, floor time.Time) bool`. Task 7 re-wires with a fake `now` for handshake-level tests.

- [ ] **Step 1: Write the failing tests**

`go/internal/agent/mtls/session_ticket_test.go`:

```go
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

func TestClientCertWindowRoundTrip(t *testing.T) {
	nb := time.Unix(1700000000, 0)
	na := time.Unix(1710000000, 0)
	ss := &tls.SessionState{}
	appendClientCertWindow(ss, tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{NotBefore: nb, NotAfter: na}},
	})
	gotNB, gotNA, ok := clientCertWindowFromExtra(ss)
	if !ok {
		t.Fatal("window not found after append")
	}
	if !gotNB.Equal(nb) || !gotNA.Equal(na) {
		t.Errorf("window = [%v, %v], want [%v, %v]", gotNB, gotNA, nb, na)
	}
}

func TestClientCertWindowNoPeerCertAppendsNothing(t *testing.T) {
	ss := &tls.SessionState{}
	appendClientCertWindow(ss, tls.ConnectionState{})
	if len(ss.Extra) != 0 {
		t.Errorf("Extra = %v, want empty", ss.Extra)
	}
	if _, _, ok := clientCertWindowFromExtra(ss); ok {
		t.Error("found a window in an empty session state")
	}
}

func TestClientCertWindowIgnoresForeignAndMalformedEntries(t *testing.T) {
	nb := time.Unix(1700000000, 0)
	na := time.Unix(1710000000, 0)
	ss := &tls.SessionState{Extra: [][]byte{
		[]byte("some-other-component:opaque"),
		[]byte(ticketMetaPrefix + "short"),       // right prefix, wrong length
		[]byte("wendy-mtls/2:0123456789abcdef"),  // future version — must not parse as v1
	}}
	if _, _, ok := clientCertWindowFromExtra(ss); ok {
		t.Fatal("parsed a window out of foreign/malformed entries")
	}
	appendClientCertWindow(ss, tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{{NotBefore: nb, NotAfter: na}},
	})
	gotNB, _, ok := clientCertWindowFromExtra(ss)
	if !ok || !gotNB.Equal(nb) {
		t.Errorf("valid entry not found among foreign entries: ok=%v nb=%v", ok, gotNB)
	}
}

func TestResumableClientWindow(t *testing.T) {
	base := time.Unix(1700000000, 0)
	nb, na := base, base.Add(30*24*time.Hour)
	cases := []struct {
		name    string
		realNow time.Time
		floor   time.Time
		want    bool
	}{
		{"inside window", base.Add(time.Hour), time.Time{}, true},
		{"expired", na.Add(time.Second), time.Time{}, false},
		{"expired despite floor", na.Add(time.Second), na.Add(48 * time.Hour), false},
		{"not yet valid, no floor", base.Add(-time.Hour), time.Time{}, false},
		{"stuck clock rescued by floor", base.Add(-30 * 24 * time.Hour), base, true},
		// Floor advancement is capped at floor+maxClockSkewTolerance: a cert
		// starting further in the future than that stays non-resumable.
		{"future cert beyond skew cap", base.Add(-30 * 24 * time.Hour), base.Add(-2 * maxClockSkewTolerance), false},
	}
	for _, tc := range cases {
		if got := resumableClientWindow(nb, na, tc.realNow, tc.floor); got != tc.want {
			t.Errorf("%s: resumable = %v, want %v", tc.name, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./go/internal/agent/mtls/ -run 'TestClientCertWindow|TestResumableClientWindow' -v`
Expected: FAIL to build — helpers undefined.

- [ ] **Step 3: Write the implementation**

`go/internal/agent/mtls/session_ticket.go`:

```go
package mtls

import (
	"crypto/tls"
	"encoding/binary"
	"time"
)

// ticketMetaPrefix tags this package's entry in SessionState.Extra. Extra is a
// shared list any component may append to, so the prefix both namespaces our
// entry and versions its layout; UnwrapSession treats an absent or
// unknown-version entry as "decline resumption".
const ticketMetaPrefix = "wendy-mtls/1:"

// wireSessionTicketChecks installs WrapSession/UnwrapSession on a server TLS
// config so that session tickets carry the verified client cert's validity
// window and resumption is DECLINED — never failed — once that window lapses.
//
// Rationale: a resumed TLS 1.3 handshake skips the certificate exchange, so
// the full ML-DSA chain verification from the original handshake is trusted
// for the ticket's lifetime (≤7 days, less on agent restart). The cheap
// re-check here bounds that trust by the cert's own validity window. Declining
// (returning nil, nil) downgrades to a full handshake, where
// VerifyPeerCertificate re-runs the complete verification and surfaces the
// existing error paths if the cert is genuinely bad — a stale ticket
// self-heals instead of hard-failing on every retry.
//
// now is injectable for handshake-level tests; production passes time.Now.
func wireSessionTicketChecks(cfg *tls.Config, notBeforeFloor time.Time, now func() time.Time) {
	cfg.WrapSession = func(cs tls.ConnectionState, ss *tls.SessionState) ([]byte, error) {
		appendClientCertWindow(ss, cs)
		return cfg.EncryptTicket(cs, ss)
	}
	cfg.UnwrapSession = func(identity []byte, cs tls.ConnectionState) (*tls.SessionState, error) {
		ss, err := cfg.DecryptTicket(identity, cs)
		if err != nil || ss == nil {
			return nil, nil // undecryptable (e.g. pre-restart ticket) → full handshake
		}
		notBefore, notAfter, ok := clientCertWindowFromExtra(ss)
		if !ok || !resumableClientWindow(notBefore, notAfter, now(), notBeforeFloor) {
			return nil, nil
		}
		return ss, nil
	}
}

// appendClientCertWindow stamps the verified client leaf's validity window
// into the session state. With no peer cert (should not happen under
// RequireAnyClientCert) nothing is appended, which UnwrapSession later reads
// as "decline".
func appendClientCertWindow(ss *tls.SessionState, cs tls.ConnectionState) {
	if len(cs.PeerCertificates) == 0 {
		return
	}
	leaf := cs.PeerCertificates[0]
	meta := make([]byte, len(ticketMetaPrefix)+16)
	copy(meta, ticketMetaPrefix)
	binary.BigEndian.PutUint64(meta[len(ticketMetaPrefix):], uint64(leaf.NotBefore.Unix()))
	binary.BigEndian.PutUint64(meta[len(ticketMetaPrefix)+8:], uint64(leaf.NotAfter.Unix()))
	ss.Extra = append(ss.Extra, meta)
}

func clientCertWindowFromExtra(ss *tls.SessionState) (notBefore, notAfter time.Time, ok bool) {
	for _, entry := range ss.Extra {
		if len(entry) != len(ticketMetaPrefix)+16 || string(entry[:len(ticketMetaPrefix)]) != ticketMetaPrefix {
			continue
		}
		nb := int64(binary.BigEndian.Uint64(entry[len(ticketMetaPrefix):]))
		na := int64(binary.BigEndian.Uint64(entry[len(ticketMetaPrefix)+8:]))
		return time.Unix(nb, 0), time.Unix(na, 0), true
	}
	return time.Time{}, time.Time{}, false
}

// resumableClientWindow mirrors buildVerifyPeerCertificate's clock handling:
// expiry is checked against the real clock (the floor must never mask real
// expiry), NotBefore against effectiveVerificationTime (the floor rescues
// devices whose clock lags NTP).
func resumableClientWindow(notBefore, notAfter, realNow, floor time.Time) bool {
	if realNow.After(notAfter) {
		return false
	}
	return !effectiveVerificationTime(realNow, floor, notBefore).Before(notBefore)
}
```

In `server.go` `NewTLSConfig`, capture the config before returning and wire the checks:

```go
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		// ... existing fields unchanged ...
		VerifyPeerCertificate: buildVerifyPeerCertificate(caPool, caCerts, logger, notBeforeFloor),
	}
	// Session resumption: stamp the client cert window into tickets and
	// decline stale ones (see session_ticket.go for the security rationale).
	wireSessionTicketChecks(cfg, notBeforeFloor, time.Now)
	return cfg, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./go/internal/agent/mtls/ -v`
Expected: all PASS (new + pre-existing).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/mtls/
git commit -m "feat(mtls): session tickets carry client cert window; stale tickets decline to full handshake

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: TLS-level resumption integration tests (mtls server)

**Files:**
- Test: `go/internal/agent/mtls/resumption_test.go`

**Interfaces:**
- Consumes: `NewTLSConfig`, `wireSessionTicketChecks`, `buildVerifyPeerCertificate` (via config), test-PKI patterns from `server_test.go`.
- Produces: proof of the spec's resumption guarantees at the handshake layer.

- [ ] **Step 1: Write the tests**

`go/internal/agent/mtls/resumption_test.go`. PKI helper — ECDSA CA + server leaf + client leaf, mirroring `server_test.go`'s templates:

```go
package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type resumptionPKI struct {
	serverCertPEM, serverKeyPEM, caPEM string
	clientCert                         tls.Certificate
}

func newResumptionPKI(t *testing.T) resumptionPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Resumption Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	leaf := func(cn string, eku x509.ExtKeyUsage) (string, string, tls.Certificate) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("gen key: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create leaf: %v", err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal key: %v", err)
		}
		certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
		return certPEM, keyPEM, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	serverCertPEM, serverKeyPEM, _ := leaf("resumption-server", x509.ExtKeyUsageServerAuth)
	_, _, clientCert := leaf("resumption-client", x509.ExtKeyUsageClientAuth)
	return resumptionPKI{
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		caPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		clientCert:    clientCert,
	}
}

// resumptionEnv serves the given config and reports each connection's
// server-side DidResume; verifies counts full VerifyPeerCertificate runs.
type resumptionEnv struct {
	addr        string
	cfg         *tls.Config
	clientCert  tls.Certificate
	verifyCount *atomic.Int32
	srvResumed  chan bool
}

func newResumptionEnv(t *testing.T) *resumptionEnv {
	t.Helper()
	pki := newResumptionPKI(t)
	cfg, err := NewTLSConfig(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM, nil, time.Time{})
	if err != nil {
		t.Fatalf("NewTLSConfig: %v", err)
	}
	env := &resumptionEnv{cfg: cfg, verifyCount: new(atomic.Int32), srvResumed: make(chan bool, 16)}
	inner := cfg.VerifyPeerCertificate
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, chains [][]*x509.Certificate) error {
		env.verifyCount.Add(1)
		return inner(rawCerts, chains)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	env.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc := tls.Server(c, cfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				env.srvResumed <- tc.ConnectionState().DidResume
				tc.Write([]byte{1})
				buf := make([]byte, 1)
				tc.Read(buf)
			}(c)
		}
	}()
	// The client mirrors grpcclient's config shape: cert presented,
	// hostname verification off (test CA has no SANs for 127.0.0.1).
	env.clientCert = pki.clientCert
	return env
}
```

```go
func (env *resumptionEnv) dial(t *testing.T, cache tls.ClientSessionCache) (clientResumed, serverResumed bool) {
	t.Helper()
	conn, err := tls.Dial("tcp", env.addr, &tls.Config{
		Certificates:       []tls.Certificate{env.clientCert},
		InsecureSkipVerify: true,
		ClientSessionCache: cache,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if v := conn.ConnectionState().Version; v != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS %x, want TLS 1.3", v)
	}
	return conn.ConnectionState().DidResume, <-env.srvResumed
}

func TestResumptionSecondConnectionResumes(t *testing.T) {
	env := newResumptionEnv(t)
	cache := tls.NewLRUClientSessionCache(4)

	c1, s1 := env.dial(t, cache)
	if c1 || s1 {
		t.Fatalf("first connection resumed (client=%v server=%v)", c1, s1)
	}
	c2, s2 := env.dial(t, cache)
	if !c2 || !s2 {
		t.Fatalf("second connection did not resume (client=%v server=%v)", c2, s2)
	}
	if n := env.verifyCount.Load(); n != 1 {
		t.Errorf("full ML-DSA verification ran %d times, want exactly 1", n)
	}
}

func TestResumptionDeclinedWhenCertWindowLapses(t *testing.T) {
	env := newResumptionEnv(t)
	cache := tls.NewLRUClientSessionCache(4)
	env.dial(t, cache)

	// Re-wire with a clock far past the client cert's NotAfter: the server
	// must DECLINE the ticket and complete a FULL handshake — not error out.
	wireSessionTicketChecks(env.cfg, time.Time{}, func() time.Time {
		return time.Now().Add(3 * 365 * 24 * time.Hour)
	})
	c2, s2 := env.dial(t, cache)
	if c2 || s2 {
		t.Fatalf("stale-window ticket resumed (client=%v server=%v)", c2, s2)
	}
	if n := env.verifyCount.Load(); n != 2 {
		t.Errorf("full verification ran %d times, want 2 (decline forces re-verify)", n)
	}
}

func TestResumptionDeclinedWithoutWindowMetadata(t *testing.T) {
	env := newResumptionEnv(t)
	cache := tls.NewLRUClientSessionCache(4)

	// Issue tickets WITHOUT the window stamp (simulates a garbled/foreign
	// ticket): resumption must be declined, connection must still succeed.
	env.cfg.WrapSession = func(cs tls.ConnectionState, ss *tls.SessionState) ([]byte, error) {
		return env.cfg.EncryptTicket(cs, ss)
	}
	env.dial(t, cache)
	c2, s2 := env.dial(t, cache)
	if c2 || s2 {
		t.Fatalf("metadata-less ticket resumed (client=%v server=%v)", c2, s2)
	}
}

func TestResumptionTicketsDisabledStillConnects(t *testing.T) {
	env := newResumptionEnv(t)
	env.cfg.SessionTicketsDisabled = true
	cache := tls.NewLRUClientSessionCache(4)

	env.dial(t, cache)
	c2, s2 := env.dial(t, cache)
	if c2 || s2 {
		t.Fatalf("resumed with tickets disabled (client=%v server=%v)", c2, s2)
	}
}
```

- [ ] **Step 2: Run the tests**

Run: `go test ./go/internal/agent/mtls/ -run TestResumption -v`
Expected: all PASS. If `TestResumptionSecondConnectionResumes` flakes on the ticket arriving after `Read` returns, the server's post-write `Read` (waiting for client close) is the synchronization point — verify it is present before debugging further.

- [ ] **Step 3: Commit**

```bash
git add go/internal/agent/mtls/resumption_test.go
git commit -m "test(mtls): handshake-level resumption, decline, and fallback coverage

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: gRPC end-to-end test — `ConnectWithTLSAndPins` resumes against `mtls.NewServer`

**Files:**
- Test: `go/internal/cli/grpcclient/resumption_integration_test.go`

**Interfaces:**
- Consumes: `mtls.NewServer(certPEM, chainPEM, keyPEM, logger, notBeforeFloor, expectedOrgID, orgMode, extraOpts...)`, `interceptor.OrgModeOff`, `grpcclient.ConnectWithTLSAndPins`, `config.CertificateInfo`, `agentpb`.
- Produces: proof that the real CLI path (file store) resumes end-to-end and that the mTLS interceptors accept resumed connections.

- [ ] **Step 1: Write the test**

`go/internal/cli/grpcclient/resumption_integration_test.go`, package `grpcclient_test`. Reuse the PKI shape from Task 7 (CA + server leaf + client leaf as PEM; the client leaf/key also PEM-encoded this time). Key facts making this work: `ExpectedOrgID: 0` in the client verifier accepts any org, and `OrgModeOff` server-side skips org checks while still requiring verified mTLS peer credentials.

```go
package grpcclient_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/agent/mtls"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// versionService records the TLS resumption state of each call's transport.
type versionService struct {
	agentpb.UnimplementedWendyAgentServiceServer
	mu      sync.Mutex
	resumed []bool
}

func (s *versionService) GetAgentVersion(ctx context.Context, _ *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	p, _ := peer.FromContext(ctx)
	info := p.AuthInfo.(credentials.TLSInfo)
	s.mu.Lock()
	s.resumed = append(s.resumed, info.State.DidResume)
	s.mu.Unlock()
	return &agentpb.GetAgentVersionResponse{Version: "test"}, nil
}

func TestConnectWithTLSResumesAcrossConnections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_TLS_SESSION_STORE", "file")

	pki := newIntegrationPKI(t) // same generator as Task 7, PEM for client too
	srv, err := mtls.NewServer(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM,
		nil, time.Time{}, 0, interceptor.OrgModeOff)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	svc := &versionService{}
	agentpb.RegisterWendyAgentServiceServer(srv, svc)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(srv.Stop)

	certInfo := &config.CertificateInfo{
		PemCertificate:      pki.clientCertPEM,
		PemPrivateKey:       pki.clientKeyPEM,
		PemCertificateChain: pki.caPEM,
	}
	call := func() {
		conn, err := grpcclient.ConnectWithTLSAndPins(context.Background(), ln.Addr().String(), certInfo, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{}); err != nil {
			t.Fatalf("GetAgentVersion: %v", err)
		}
	}

	call() // full handshake; ticket persists asynchronously afterwards

	// The Cache lives inside the connection we just closed; its async persist
	// races this second dial. Poll until resumption is observed (bounded).
	deadline := time.Now().Add(10 * time.Second)
	for {
		call()
		svc.mu.Lock()
		n := len(svc.resumed)
		resumed := svc.resumed[n-1]
		svc.mu.Unlock()
		if resumed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no connection resumed within 10s — ticket never persisted or offered")
		}
		time.Sleep(100 * time.Millisecond)
	}
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.resumed[0] {
		t.Error("first connection unexpectedly resumed")
	}
}
```

`newIntegrationPKI(t)` in the same file (Task 7's equivalent helper is in another package and deliberately unexported — do not export it across the agent/CLI boundary):

```go
type integrationPKI struct {
	serverCertPEM, serverKeyPEM, caPEM string
	clientCertPEM, clientKeyPEM        string
}

func newIntegrationPKI(t *testing.T) integrationPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Resumption E2E CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(48 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}

	leaf := func(cn string, eku x509.ExtKeyUsage) (certPEM, keyPEM string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("gen key: %v", err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create leaf: %v", err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshal key: %v", err)
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	}

	serverCertPEM, serverKeyPEM := leaf("resumption-e2e-server", x509.ExtKeyUsageServerAuth)
	clientCertPEM, clientKeyPEM := leaf("resumption-e2e-client", x509.ExtKeyUsageClientAuth)
	return integrationPKI{
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		caPEM:         string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		clientCertPEM: clientCertPEM,
		clientKeyPEM:  clientKeyPEM,
	}
}
```

(Add `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/x509`, `crypto/x509/pkix`, `encoding/pem`, `math/big` to the imports.)

- [ ] **Step 2: Run the test**

Run: `go test ./go/internal/cli/grpcclient/ -run TestConnectWithTLSResumes -v`
Expected: PASS. This also proves the mTLS interceptor accepted a resumed connection (the RPC would fail with Unauthenticated if `PeerCertificates` were missing).

- [ ] **Step 3: Run both touched packages' full suites**

Run: `go test ./go/internal/cli/grpcclient/ ./go/internal/agent/mtls/ ./go/internal/cli/tlscache/`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/grpcclient/resumption_integration_test.go
git commit -m "test(grpcclient): end-to-end CLI→agent session resumption over gRPC

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: Mesh client in-memory session cache

**Files:**
- Modify: `go/internal/agent/mtls/server.go:118-134` (`NewClientTLSConfig`)
- Test: append to `go/internal/agent/mtls/session_ticket_test.go`

**Interfaces:**
- Consumes: existing `NewClientTLSConfig`.
- Produces: package-level `var meshSessionCache = tls.NewLRUClientSessionCache(64)` shared by all mesh client configs (the agent is long-lived and holds a single client identity, so a process-wide cache is correct and per-config caches would never hit).

- [ ] **Step 1: Write the failing test**

Append to `session_ticket_test.go` (reuse `newResumptionPKI` from Task 7 — same package):

```go
func TestNewClientTLSConfigSharesSessionCache(t *testing.T) {
	pki := newResumptionPKI(t)
	a, err := NewClientTLSConfig(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM, nil)
	if err != nil {
		t.Fatalf("NewClientTLSConfig: %v", err)
	}
	b, err := NewClientTLSConfig(pki.serverCertPEM, pki.caPEM, pki.serverKeyPEM, nil)
	if err != nil {
		t.Fatalf("NewClientTLSConfig: %v", err)
	}
	if a.ClientSessionCache == nil {
		t.Fatal("ClientSessionCache not set on mesh client config")
	}
	if a.ClientSessionCache != b.ClientSessionCache {
		t.Error("mesh client configs do not share one session cache; per-config caches never hit (configs are built per dial)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/mtls/ -run TestNewClientTLSConfigShares -v`
Expected: FAIL — `ClientSessionCache` nil.

- [ ] **Step 3: Implement**

In `server.go`, above `NewClientTLSConfig`:

```go
// meshSessionCache lets agent→agent mesh dials resume TLS sessions. One
// process-wide cache: mesh TLS configs are constructed per dial, so a
// per-config cache would never produce a hit, and the agent presents a single
// client identity so cross-identity ticket reuse cannot arise.
var meshSessionCache = tls.NewLRUClientSessionCache(64)
```

and add `ClientSessionCache: meshSessionCache,` to the `tls.Config` literal returned by `NewClientTLSConfig`.

- [ ] **Step 4: Run tests**

Run: `go test ./go/internal/agent/mtls/ -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/mtls/
git commit -m "feat(mtls): shared in-memory session cache for mesh client dials

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: Full verification, gofmt, push, draft PR

**Files:**
- No new files; verification + PR creation.

- [ ] **Step 1: gofmt (mandatory pre-push)**

Run from repo root: `gofmt -l .`
Expected: no output. If files are listed: `gofmt -w <files>`, re-run tests for touched packages, `git commit -am "style: gofmt"` (with the Co-Authored-By trailer).

- [ ] **Step 2: Build + full test pass on touched packages and their dependents**

```bash
go build ./go/...
go test ./go/internal/cli/tlscache/ ./go/internal/cli/grpcclient/ ./go/internal/agent/mtls/ ./go/internal/cli/commands/ ./go/internal/agent/services/
```
Expected: build clean; all PASS. (`commands` and `services` are the main consumers of the modified constructors — they must still compile and pass.)

- [ ] **Step 3: Push and open a draft PR**

```bash
git push -u origin ed/tls-session-resumption
gh pr create --draft --title "TLS 1.3 session resumption: repeat CLI→agent connects skip the ML-DSA handshake" --body "$(cat <<'EOF'
## Summary
Every `wendy` invocation currently redoes the full ML-DSA mTLS handshake (~2.2s on Jetson/Pi). This PR persists TLS 1.3 session tickets across CLI processes so repeat connects resume with a PSK handshake instead.

- **New `go/internal/cli/tlscache`**: `tls.ClientSessionCache` keyed by `SHA256(target | client-leaf-fingerprint)`. macOS stores tickets in the **Keychain** (via `security -i`, secret never on argv); Linux/Windows use `0600` files under `~/.wendy/tls-sessions/` (the client key already lives in `~/.wendy/config.json`, so no new exposure class). `WENDY_TLS_SESSION_STORE=keychain|file|off` overrides.
- **Agent `mtls.NewTLSConfig`**: `WrapSession` stamps the verified client cert's validity window into the ticket (`wendy-mtls/1:` Extra entry); `UnwrapSession` **declines** (never fails) resumption when the window has lapsed — stale tickets self-heal into full handshakes, mirroring `notBeforeFloor` clock-skew semantics via the shared `effectiveVerificationTime`.
- **Mesh**: agent→agent client configs share one in-memory LRU session cache.
- `WENDY_TLS_DEBUG=1` now logs `resumed=true/false` per connection.

Design: `specs/2026-08-07-tls-session-resumption-design.md`

## Compatibility
Old CLI ↔ new agent and new CLI ↔ old agent both simply full-handshake. Agent restart invalidates tickets (per-process ticket keys) → one full handshake per device.

## Verification
- [x] Unit + handshake-level integration tests (resume / decline / disabled / keyed-by-cert)
- [x] gRPC end-to-end: `ConnectWithTLSAndPins` resumes against `mtls.NewServer`; interceptors accept resumed conns
- [ ] On-device: `WENDY_TIMING=1` before/after on Orin Nano (expect mTLS-attempts phase ~2.2s → low ms on repeat connects)
- [ ] macOS: confirm Keychain reads never prompt (`security find-generic-password` ACL) on a real Mac

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Update project memory** with the PR number and remaining on-device verification steps.

---

## Self-Review Notes

- **Spec coverage:** cache pkg + keying (Tasks 1–3), Keychain default / file fallback / env override (Task 2), client wiring + debug logging (Task 4), server Wrap/Unwrap with floor semantics + decline-not-fail (Tasks 5–6), handshake + gRPC integration proofs incl. tickets-disabled compat (Tasks 7–8), mesh LRU (Task 9), on-device `WENDY_TIMING` gate (Task 10 PR checklist). Async `Put` (spec §1) is in Task 3 with `Flush()` for determinism.
- **Known intentional duplication:** the ECDSA test-PKI generator appears in Tasks 3, 7, and 8 (different packages; kept unexported per package rather than exporting test helpers across the agent/CLI boundary).
- **Verify during Task 2 execution:** the exact `security -i` stdin syntax against a real macOS Keychain (the fake only pins our invocation contract). The spec's "no prompts" property is a PR-checklist item, not a unit test.
