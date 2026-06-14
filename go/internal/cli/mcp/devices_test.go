package mcp

import (
	"context"
	"testing"

	grpcclient "github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

func TestResolveConnFallsBackToActive(t *testing.T) {
	s := New(nil, nil)
	if _, err := s.resolveConn(context.Background(), ""); err == nil {
		t.Fatalf("expected error when nothing connected")
	}
}

func TestResolveConnUsesCacheByName(t *testing.T) {
	s := New(nil, nil)
	ac := &grpcclient.AgentConnection{Host: "dev1"}
	s.cacheConn("dev1", &cachedConn{host: "dev1", conn: ac})
	got, err := s.resolveConn(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if got != ac {
		t.Fatalf("resolveConn returned wrong connection")
	}
}
