package mount

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
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

func TestWebdavHandlerRoundTrip(t *testing.T) {
	c, _ := newTestFSClient(t)
	handler := &webdav.Handler{
		FileSystem: NewWebdavFS(c),
		LockSystem: webdav.NewMemLS(),
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// PUT a file.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/hello.txt", strings.NewReader("dav round trip"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status: %d", resp.StatusCode)
	}

	// GET it back.
	getResp, err := http.Get(srv.URL + "/hello.txt")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer getResp.Body.Close()
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "dav round trip" {
		t.Fatalf("GET body = %q, want %q", body, "dav round trip")
	}

	// PROPFIND the root lists the file (depth 1).
	pf, _ := http.NewRequest("PROPFIND", srv.URL+"/", nil)
	pf.Header.Set("Depth", "1")
	pfResp, err := http.DefaultClient.Do(pf)
	if err != nil {
		t.Fatalf("PROPFIND: %v", err)
	}
	defer pfResp.Body.Close()
	pfBody, _ := io.ReadAll(pfResp.Body)
	if pfResp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status: %d", pfResp.StatusCode)
	}
	if !strings.Contains(string(pfBody), "hello.txt") {
		t.Fatalf("PROPFIND body missing hello.txt: %s", pfBody)
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
