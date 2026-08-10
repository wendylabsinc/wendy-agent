package tlscache

import (
	"runtime"
	"testing"
)

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

// The darwin default must not be the Keychain: `security add-generic-password`
// has no no-interaction flag (verified against `security help`), so a context
// with no resolvable keychain — a sandboxed or non-login session — makes macOS
// throw a blocking "A keychain cannot be found to store ..." modal from Put's
// background goroutine. A latency cache must never be able to do that, so
// darwin gets the same file backend as every other platform.
func TestDarwinDefaultIsFileStore(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("asserts the darwin-specific platform default")
	}
	t.Setenv("HOME", t.TempDir()) // keep newFileStore off the real ~/.wendy
	t.Setenv("WENDY_TLS_SESSION_STORE", "")
	if _, ok := newDefaultStore().(*fileStore); !ok {
		t.Errorf("darwin default = %T, want *fileStore", newDefaultStore())
	}
}

// The Keychain backend stays reachable for anyone who wants at-rest encryption
// while the keychain is locked, but only by opting in explicitly. It has no
// exported concrete type to assert against (secretstore.NewKeychain's return
// is unexported), so the opt-in is verified negatively: it must not be the
// *fileStore used by default.
func TestDarwinKeychainRequiresExplicitOptIn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain backend is darwin-only")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("WENDY_TLS_SESSION_STORE", "keychain")
	if _, ok := newDefaultStore().(*fileStore); ok {
		t.Error("store=keychain: got *fileStore, want the Keychain-backed store")
	}
}
