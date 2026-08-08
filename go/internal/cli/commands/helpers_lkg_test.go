package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// seedLKGCache installs a deviceCacheLoadFn serving one entry, stale by
// stalenessFactor×TTL (0 = fresh).
func seedLKGCache(t *testing.T, e discoverycache.Entry, stale time.Duration) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devices.json")
	c, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	now := time.Now().Add(-stale)
	c.Upsert(e, now)
	if err := c.Flush(now); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	orig := deviceCacheLoadFn
	deviceCacheLoadFn = func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }
	t.Cleanup(func() { deviceCacheLoadFn = orig })
}

func TestConnectFastPathStaleEntryZeroResolution(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true, OrgID: 2,
	}, 3*discoverycache.TTL) // well past the display TTL

	resolverCalls := 0
	origLookup, origBrowse := osLookupHostFn, lanBrowseFn
	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		resolverCalls++
		return nil, errors.New("resolver must not run on an LKG hit")
	}
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		resolverCalls++
		return nil, errors.New("browse must not run on an LKG hit")
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		if e.IP != "10.0.0.9" || e.Port != 50052 {
			t.Errorf("LKG got entry %s:%d, want 10.0.0.9:50052", e.IP, e.Port)
		}
		return want, nil, true
	}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		t.Errorf("general ladder ran despite LKG success (addr %s)", addr)
		return nil, nil, errors.New("unreachable")
	}
	t.Cleanup(func() {
		osLookupHostFn, lanBrowseFn = origLookup, origBrowse
		dialAgentLKGFn, dialAgentLadderFn = origLKG, origLadder
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "orin.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want the LKG connection", conn, err)
	}
	if resolverCalls != 0 {
		t.Errorf("resolver invoked %d times on a stale-entry LKG hit, want 0 (any-age fast path)", resolverCalls)
	}
}

func TestConnectFastPathLKGFailureFallsThroughToLadder(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true,
	}, 0)

	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		return nil, nil, false // dead IP: pre-check failed
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddr string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		ladderAddr = addr
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() {
		dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "orin.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection after LKG fall-through", conn, err)
	}
	if ladderAddr != "10.0.0.9:50051" {
		t.Errorf("ladder addr = %q, want cached-IP fallback 10.0.0.9:50051", ladderAddr)
	}
}

func TestConnectFastPathNonMTLSEntrySkipsLKG(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "pi.local", IP: "10.0.0.7", Port: 50051, MTLS: false,
	}, 0)
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry) (*grpcclient.AgentConnection, error, bool) {
		t.Error("LKG ran for a non-mTLS entry")
		return nil, nil, false
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, addr string) (*grpcclient.AgentConnection, error, error) {
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() { dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach })

	if conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "pi.local:50051"); err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection", conn, err)
	}
}
