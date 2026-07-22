//go:build windows

package t234

import (
	"errors"
	"testing"
)

// TestRawSyncError guards the Windows flush behavior: FlushFileBuffers is
// supported on physical-drive handles, so there is no ENOTTY-style special
// case — any error must surface.
func TestRawSyncError(t *testing.T) {
	if err := rawSyncError(`\\.\PhysicalDrive42`, nil); err != nil {
		t.Fatalf("nil sync error should be nil, got %v", err)
	}
	sentinel := errors.New("flush failed")
	if err := rawSyncError(`\\.\PhysicalDrive42`, sentinel); err == nil {
		t.Fatal("a real sync error must not be swallowed")
	} else if !errors.Is(err, sentinel) {
		t.Fatalf("returned error should wrap the cause, got %v", err)
	}
}
