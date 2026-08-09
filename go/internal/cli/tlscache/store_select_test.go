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
