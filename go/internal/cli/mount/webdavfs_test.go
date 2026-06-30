package mount

import (
	"context"
	"io"
	"os"
	"testing"
)

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
