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

func TestDarwinDefaultIsKeychain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("keychain is the darwin-only platform default")
	}
	t.Setenv("WENDY_TLS_SESSION_STORE", "")
	// The Keychain backend has no exported concrete type to assert against
	// (secretstore.NewKeychain's return is an unexported type), so the
	// darwin platform default is verified negatively: it must not be the
	// *fileStore fallback used everywhere else.
	if _, ok := newDefaultStore().(*fileStore); ok {
		t.Error("darwin default = *fileStore, want the Keychain-backed store")
	}
}
