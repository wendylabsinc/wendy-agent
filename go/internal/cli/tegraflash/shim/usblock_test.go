//go:build darwin || linux

package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/tegraflash/flasher"
)

// TestAcquireUSBLockSerializes verifies that a second acquisition (modeling a
// second shim process — flock treats each open file description independently)
// blocks while the first holds the lock and acquires once released.
func TestAcquireUSBLockSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usb.lock")
	t.Setenv(flasher.EnvADBLock, path)

	release := acquireUSBLock()

	acquired := make(chan func())
	go func() { acquired <- acquireUSBLock() }()

	select {
	case <-acquired:
		t.Fatal("second acquireUSBLock succeeded while the first still held the lock")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case release2 := <-acquired:
		release2()
	case <-time.After(2 * time.Second):
		t.Fatal("second acquireUSBLock did not acquire after the first released")
	}
}

// TestTryAcquireUSBLock verifies the non-blocking variant: busy while a peer
// holds the lock, acquired once it is free.
func TestTryAcquireUSBLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usb.lock")
	t.Setenv(flasher.EnvADBLock, path)

	release := acquireUSBLock()
	if _, busy := tryAcquireUSBLock(); !busy {
		t.Fatal("tryAcquireUSBLock acquired a held lock")
	}

	release()
	release2, busy := tryAcquireUSBLock()
	if busy {
		t.Fatal("tryAcquireUSBLock reported busy on a free lock")
	}
	release2()
}

// TestAcquireUSBLockCreatesFile verifies the lock file is created on demand.
func TestAcquireUSBLockCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist-yet.lock")
	t.Setenv(flasher.EnvADBLock, path)

	release := acquireUSBLock()
	defer release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file not created: %v", err)
	}
}

// TestUSBLockPath verifies the env var wins and the env-less fallback lands in
// the user's own cache dir — shims spawned outside flasher.Run (the cached
// bundle's adb is permanently linked to wendy) must still serialize, and the
// world-writable system temp dir is only the last resort (another user could
// pre-plant its well-known name).
func TestUSBLockPath(t *testing.T) {
	t.Setenv(flasher.EnvADBLock, "/p/lock")
	if got := usbLockPath(); got != "/p/lock" {
		t.Errorf("usbLockPath with env = %q, want /p/lock", got)
	}

	// Env unset, cache dir available: the lock lives under the user cache dir.
	home := t.TempDir()
	t.Setenv(flasher.EnvADBLock, "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "") // linux: force UserCacheDir onto HOME
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("UserCacheDir with HOME set: %v", err)
	}
	want := filepath.Join(cache, "wendy", "adb-usb.lock")
	if got := usbLockPath(); got != want {
		t.Errorf("usbLockPath cache fallback = %q, want %q", got, want)
	}

	// No cache dir resolvable: last resort is a per-uid temp path.
	t.Setenv("HOME", "")
	want = filepath.Join(os.TempDir(), fmt.Sprintf("wendy-adb-usb-%d.lock", os.Getuid()))
	if got := usbLockPath(); got != want {
		t.Errorf("usbLockPath temp fallback = %q, want %q", got, want)
	}
}
