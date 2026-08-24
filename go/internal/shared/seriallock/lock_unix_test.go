//go:build unix

package seriallock

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireBlocksSecondLock(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "seriallock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	path := f.Name()
	f.Close()

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() error = %v, want success on an unheld file", err)
	}
	defer first.Release()

	_, err = Acquire(path)
	if err == nil {
		t.Fatal("Acquire() = nil error, want failure while the first lock is held")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("Acquire() error = %v, want it to satisfy errors.Is(err, ErrLocked)", err)
	}
	if !strings.Contains(err.Error(), "in use by another program") {
		t.Errorf("Acquire() error = %q, want it to mention the file is in use", err.Error())
	}
}

func TestAcquireMissingDeviceIsNotErrLocked(t *testing.T) {
	// A device that doesn't exist yet (e.g. mid re-enumeration) is a
	// different failure mode from "someone else holds it" — callers need
	// to be able to tell them apart to keep retrying the former.
	path := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := Acquire(path)
	if err == nil {
		t.Fatal("Acquire() = nil error, want failure on a nonexistent path")
	}
	if errors.Is(err, ErrLocked) {
		t.Errorf("Acquire() error = %v, want it to NOT satisfy errors.Is(err, ErrLocked) for a missing device", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Acquire() error = %v, want it to satisfy errors.Is(err, fs.ErrNotExist)", err)
	}
}

func TestReleaseAllowsReacquire(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "seriallock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	path := f.Name()
	f.Close()

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() error = %v, want success on an unheld file", err)
	}
	first.Release()

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire() error = %v, want success after the first lock was released", err)
	}
	second.Release()
}

func TestReleaseNilLock(t *testing.T) {
	var l *Lock
	l.Release() // must not panic
}
