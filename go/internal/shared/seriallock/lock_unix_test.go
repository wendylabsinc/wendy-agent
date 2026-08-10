//go:build unix

package seriallock

import (
	"os"
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
	if !strings.Contains(err.Error(), "in use by another program") {
		t.Errorf("Acquire() error = %q, want it to mention the file is in use", err.Error())
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
