package ipcam

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := NewCredentialStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("load on missing file: %v", err)
	}
	if s.Has("ec:71:db:2a:ae:7e") {
		t.Fatal("Has() true before any Set")
	}
	if err := s.Set("ec:71:db:2a:ae:7e", Credential{Username: "admin", Password: "hunter2"}); err != nil {
		t.Fatalf("set: %v", err)
	}

	reloaded := NewCredentialStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := reloaded.Get("ec:71:db:2a:ae:7e")
	if !ok {
		t.Fatal("credential missing after reload")
	}
	if got.Username != "admin" || got.Password != "hunter2" {
		t.Fatalf("credential = %+v, want admin/hunter2", got)
	}
}

// Credentials must never be group- or world-readable.
func TestCredentialFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "credentials.json")
	s := NewCredentialStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("aa:bb:cc:dd:ee:ff", Credential{Username: "u", Password: "p"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Fatalf("directory mode = %o, want 700", perm)
	}
}

func TestCredentialDelete(t *testing.T) {
	s := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("aa:bb:cc:dd:ee:ff", Credential{Username: "u", Password: "p"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Delete("aa:bb:cc:dd:ee:ff"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if s.Has("aa:bb:cc:dd:ee:ff") {
		t.Fatal("Has() true after Delete")
	}
	// Deleting an unknown MAC succeeds: the caller wanted no credential stored,
	// and there is none.
	if err := s.Delete("no:su:ch:ma:c0:00"); err != nil {
		t.Fatalf("delete unknown: %v", err)
	}
}

func TestCredentialSetOverwrites(t *testing.T) {
	s := NewCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	mac := "aa:bb:cc:dd:ee:ff"
	if err := s.Set(mac, Credential{Username: "old", Password: "old"}); err != nil {
		t.Fatalf("first set: %v", err)
	}
	if err := s.Set(mac, Credential{Username: "new", Password: "new"}); err != nil {
		t.Fatalf("second set: %v", err)
	}
	got, _ := s.Get(mac)
	if got.Username != "new" || got.Password != "new" {
		t.Fatalf("credential = %+v, want the second value", got)
	}
}

// A store whose file holds an empty JSON object must still be usable.
func TestCredentialLoadEmptyObject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := NewCredentialStore(path)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := s.Set("aa:bb:cc:dd:ee:ff", Credential{Username: "u"}); err != nil {
		t.Fatalf("set after empty load: %v", err)
	}
}
