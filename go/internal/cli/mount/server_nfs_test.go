package mount

import (
	"context"
	"strings"
	"testing"
)

func TestServeNFSStartsOnLoopback(t *testing.T) {
	c, _ := newTestFSClient(t)
	addr, stop, err := ServeNFS(context.Background(), NewBillyFS(c))
	if err != nil {
		t.Fatalf("ServeNFS: %v", err)
	}
	defer stop()
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("expected loopback addr, got %q", addr)
	}
}
