package mount

import (
	"context"
	"io"
	"os"
	"testing"
)

func TestWebdavFSReaddir(t *testing.T) {
	c, _ := newTestFSClient(t)
	fs := NewWebdavFS(c)
	ctx := context.Background()
	if err := fs.Mkdir(ctx, "d1", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mkdir(ctx, "d2", 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := fs.OpenFile(ctx, "", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile root: %v", err)
	}
	defer dir.Close()
	infos, err := dir.Readdir(-1)
	if err != nil {
		t.Fatalf("Readdir: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(infos))
	}
}

func TestWebdavFSWriteRead(t *testing.T) {
	c, _ := newTestFSClient(t)
	fs := NewWebdavFS(c)
	ctx := context.Background()

	f, err := fs.OpenFile(ctx, "dav.txt", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := f.Write([]byte("dav payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = f.Close()

	rf, err := fs.OpenFile(ctx, "dav.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile read: %v", err)
	}
	got, _ := io.ReadAll(rf)
	_ = rf.Close()
	if string(got) != "dav payload" {
		t.Fatalf("got %q", got)
	}

	if err := fs.Mkdir(ctx, "sub", 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := fs.Stat(ctx, "sub"); err != nil {
		t.Fatalf("Stat: %v", err)
	}
}
