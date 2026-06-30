package mount

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestServeWebdavStartsOnLoopback(t *testing.T) {
	c, _ := newTestFSClient(t)
	addr, stop, err := ServeWebdav(context.Background(), NewWebdavFS(c))
	if err != nil {
		t.Fatalf("ServeWebdav: %v", err)
	}
	defer stop()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected loopback addr, got %q", addr)
	}
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode >= 500 {
		t.Fatalf("unexpected server error status: %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
