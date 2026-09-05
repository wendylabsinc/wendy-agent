package flock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTryLockContendsAcrossHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	a, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if locked, err := TryLock(a); err != nil || !locked {
		t.Fatalf("first TryLock = (%v, %v), want acquired", locked, err)
	}
	if locked, err := TryLock(b); err != nil || locked {
		t.Fatalf("contended TryLock = (%v, %v), want (false, nil)", locked, err)
	}
	if err := Unlock(a); err != nil {
		t.Fatal(err)
	}
	if locked, err := TryLock(b); err != nil || !locked {
		t.Fatalf("TryLock after release = (%v, %v), want acquired", locked, err)
	}
}
