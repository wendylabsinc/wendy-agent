//go:build darwin || linux

package t234

import (
	"errors"
	"syscall"
	"testing"
)

// TestRawSyncError guards the macOS raw-device flush behavior: fsync on a raw
// character device (/dev/rdiskN) returns ENOTTY, which must be tolerated (the
// unbuffered writes have already landed), while any other error must surface.
func TestRawSyncError(t *testing.T) {
	if err := rawSyncError("/dev/rdisk42", nil); err != nil {
		t.Fatalf("nil sync error should be nil, got %v", err)
	}
	if err := rawSyncError("/dev/rdisk42", syscall.ENOTTY); err != nil {
		t.Fatalf("ENOTTY should be tolerated on a raw device, got %v", err)
	}
	// A wrapped ENOTTY (as os.File.Sync returns a *PathError) is still tolerated.
	if err := rawSyncError("/dev/rdisk42", &pathErr{syscall.ENOTTY}); err != nil {
		t.Fatalf("wrapped ENOTTY should be tolerated, got %v", err)
	}
	if err := rawSyncError("/dev/rdisk42", syscall.EIO); err == nil {
		t.Fatal("a real sync error (EIO) must not be swallowed")
	} else if !errors.Is(err, syscall.EIO) {
		t.Fatalf("returned error should wrap EIO, got %v", err)
	}
}

// pathErr mimics *os.PathError so errors.Is unwrapping is exercised.
type pathErr struct{ err error }

func (e *pathErr) Error() string { return "sync: " + e.err.Error() }
func (e *pathErr) Unwrap() error { return e.err }
