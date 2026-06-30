package mount

import (
	"io"
	"testing"
)

func TestBillyFSWriteReadRemove(t *testing.T) {
	c, _ := newTestFSClient(t)
	fs := NewBillyFS(c)

	f, err := fs.Create("hello.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Write([]byte("billy world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rf, err := fs.Open("hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := io.ReadAll(rf)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "billy world" {
		t.Fatalf("got %q", got)
	}
	_ = rf.Close()

	if err := fs.MkdirAll("a/b", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	infos, err := fs.ReadDir("")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("expected entries")
	}
	if err := fs.Remove("hello.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}
