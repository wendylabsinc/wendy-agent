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

// The active connection (made with the correct, possibly non-default port) is
// reused when a device arg names the same host — instead of re-dialing the bare
// hostname, which would fall back to the default port and fail.
func TestResolveConnReusesActiveConnByHost(t *testing.T) {
	s := New(nil, nil)
	ac := &grpcclient.AgentConnection{Host: "thor.local:50052"}
	s.SetConn(ac)
	got, err := s.resolveConn(context.Background(), "thor.local") // no port
	if err != nil {
		t.Fatal(err)
	}
	if got != ac {
		t.Fatalf("expected the active connection to be reused for the same host")
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
