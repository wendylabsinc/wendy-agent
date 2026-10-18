package localkey

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", FileName)
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := PublicKeyDER(first)
	b, _ := PublicKeyDER(second)
	if !bytes.Equal(a, b) {
		t.Fatal("second call generated a new key")
	}
	if _, err := x509.ParsePKIXPublicKey(a); err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("key mode %#o, want 0600", fi.Mode().Perm())
		}
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".local-operator-key-*")); len(left) > 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestLoadRefusesAReadableKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	path := filepath.Join(t.TempDir(), FileName)
	if _, err := LoadOrCreate(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("loaded a world-readable key")
	}
}
