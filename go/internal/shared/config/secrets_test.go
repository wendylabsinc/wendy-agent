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
