package mount

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestE2EBillyThroughGRPCToDisk drives the billy adapter (what the NFS server
// uses) end to end: write via the adapter, assert the bytes land in the volume
// directory on the "device", then read back through the adapter.
func TestE2EBillyThroughGRPCToDisk(t *testing.T) {
	c, root := newTestFSClient(t)
	fs := NewBillyFS(c)

	if err := fs.MkdirAll("dir", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wf, err := fs.Create("dir/nested.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := wf.Write([]byte("end to end")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = wf.Close()

	// Assert on the device side.
	onDisk := filepath.Join(root, "vol", "dir", "nested.txt")
	got, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("read on disk: %v", err)
	}
	if string(got) != "end to end" {
		t.Fatalf("on-disk content %q", got)
	}

	// Read back through the adapter.
	rf, err := fs.Open("dir/nested.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	back, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(back) != "end to end" {
		t.Fatalf("read-back %q", back)
	}
}
