package commands

import (
	"context"
	"errors"
	"net"
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
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		if e.IP != "10.0.0.9" || e.Port != 50052 {
			t.Errorf("LKG got entry %s:%d, want 10.0.0.9:50052", e.IP, e.Port)
		}
		// The pin key must be the name the caller asked for, not anything the
		// cache row supplied.
		if pinKey != "orin.local" {
			t.Errorf("LKG pin key = %q, want the requested name %q", pinKey, "orin.local")
		}
		return want, nil, lkgConnected
	}
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		t.Errorf("general ladder ran despite LKG success (addr %s)", target.Addr)
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

// TestConnectFastPathLKGHandshakeFailedFallsThroughToLadder covers the
// lkgHandshakeFailed outcome: TCP answered but the direct dial couldn't
// produce a usable mTLS connection. The host is proven alive, so the
// ordinary cached-IP ladder (plus its diagnostics and stale-cache retry)
// must still run, against the CACHED IP.
func TestConnectFastPathLKGHandshakeFailedFallsThroughToLadder(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true,
	}, 0)

	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		return nil, nil, lkgHandshakeFailed // host alive, handshake didn't pan out
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddr string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		ladderAddr = target.Addr
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

// TestConnectFastPathLKGDeadTCPFallsThroughToFreshResolution covers the
// lkgDeadTCP outcome (Finding 1): the cached IP didn't even answer TCP, so
// the connect must NOT run the cached-IP ladder against that dead IP at
// all — it must skip straight to fresh resolution (the OS resolver here)
// and run the ladder against the newly-resolved address instead.
func TestConnectFastPathLKGDeadTCPFallsThroughToFreshResolution(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "orin.local", IP: "10.0.0.9", Port: 50052, MTLS: true,
	}, 0)

	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		return nil, nil, lkgDeadTCP // cached IP is dead
	}
	origLookup, origBrowse := osLookupHostFn, lanBrowseFn
	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		return []string{"10.9.9.9"}, nil // fresh resolution finds a new address
	}
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		t.Error("mDNS browse ran despite a successful OS resolver hit")
		return nil, errors.New("must not be reached")
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddrs []string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		ladderAddrs = append(ladderAddrs, target.Addr)
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() {
		osLookupHostFn, lanBrowseFn = origLookup, origBrowse
		dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "orin.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection against the resolved address", conn, err)
	}
	if len(ladderAddrs) != 1 || ladderAddrs[0] != "10.9.9.9:50051" {
		t.Fatalf("ladder addrs = %v, want exactly one call at the resolved address 10.9.9.9:50051", ladderAddrs)
	}
	for _, a := range ladderAddrs {
		if a == "10.0.0.9:50052" || a == "10.0.0.9:50051" {
			t.Fatalf("ladder ran against the dead cached IP %q — dead-TCP outcome must skip the cached-IP ladder entirely", a)
		}
	}
}

// TestConnectFastPathLKGIneligibleDeadTCPFallsThroughToFreshResolution
// covers Finding 2 for an LKG-ineligible entry (MTLS: false, so
// dialAgentLKG never runs): the cache-derived dial still needs the same
// bounded TCP pre-check, and a dead result must fall through to fresh
// resolution rather than an unbounded fromCache ladder.
func TestConnectFastPathLKGIneligibleDeadTCPFallsThroughToFreshResolution(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "pi.local", IP: "10.0.0.7", Port: 50051, MTLS: false,
	}, 0)
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		t.Error("LKG ran for a non-mTLS entry")
		return nil, nil, lkgDeadTCP
	}
	origTCP := tcpDialTimeoutFn
	tcpDialTimeoutFn = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("no route to host") // pre-check fails: dead
	}
	origLookup, origBrowse := osLookupHostFn, lanBrowseFn
	osLookupHostFn = func(ctx context.Context, host string) ([]string, error) {
		return []string{"10.9.9.7"}, nil
	}
	lanBrowseFn = func(ctx context.Context, timeout time.Duration) ([]models.LANDevice, error) {
		t.Error("mDNS browse ran despite a successful OS resolver hit")
		return nil, errors.New("must not be reached")
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddr string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		ladderAddr = target.Addr
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() {
		osLookupHostFn, lanBrowseFn = origLookup, origBrowse
		tcpDialTimeoutFn = origTCP
		dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "pi.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection against the resolved address", conn, err)
	}
	if ladderAddr != "10.9.9.7:50051" {
		t.Errorf("ladder addr = %q, want the freshly-resolved address 10.9.9.7:50051 (dead cached IP must not be dialed)", ladderAddr)
	}
}

// TestConnectFastPathLKGIneligibleLiveTCPUsesCachedIP covers Finding 2's
// success path: an LKG-ineligible entry whose cached IP DOES answer TCP
// still gets the fromCache ladder at that cached IP, same as before this
// fix — the new pre-check must not regress the common case.
func TestConnectFastPathLKGIneligibleLiveTCPUsesCachedIP(t *testing.T) {
	seedLKGCache(t, discoverycache.Entry{
		ID: "dev-1", Hostname: "pi.local", IP: "10.0.0.7", Port: 50051, MTLS: false,
	}, 0)
	origLKG := dialAgentLKGFn
	dialAgentLKGFn = func(ctx context.Context, e discoverycache.Entry, pinKey string) (*grpcclient.AgentConnection, error, lkgOutcome) {
		t.Error("LKG ran for a non-mTLS entry")
		return nil, nil, lkgDeadTCP
	}
	origTCP := tcpDialTimeoutFn
	tcpDialTimeoutFn = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		c1, c2 := net.Pipe()
		go c2.Close()
		return c1, nil // pre-check succeeds: alive
	}
	want := &grpcclient.AgentConnection{IsMTLS: true}
	var ladderAddr string
	origLadder := dialAgentLadderFn
	dialAgentLadderFn = func(ctx context.Context, target dialTarget) (*grpcclient.AgentConnection, error, error) {
		ladderAddr = target.Addr
		return want, nil, nil
	}
	origReach := cacheFastPathReachableFn
	cacheFastPathReachableFn = func(ctx context.Context, conn *grpcclient.AgentConnection, err error) bool { return true }
	t.Cleanup(func() {
		tcpDialTimeoutFn = origTCP
		dialAgentLKGFn, dialAgentLadderFn, cacheFastPathReachableFn = origLKG, origLadder, origReach
	})

	conn, _, err := connectWithAutoTLSDiagnostics(context.Background(), "pi.local:50051")
	if err != nil || conn != want {
		t.Fatalf("connect = (%v, %v), want ladder connection", conn, err)
	}
	if ladderAddr != "10.0.0.7:50051" {
		t.Errorf("ladder addr = %q, want cached IP 10.0.0.7:50051", ladderAddr)
	}
}
