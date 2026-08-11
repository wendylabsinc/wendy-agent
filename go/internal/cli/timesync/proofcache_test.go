package clitimesync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
)

// stubQuery replaces the Roughtime query for the duration of a test and clears
// both the in-process memo and the on-disk cache around it.
func stubQuery(t *testing.T, fn func() (roughtime.Result, error)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	orig := roughtimeQueryFn
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		return fn()
	}
	t.Cleanup(func() { roughtimeQueryFn = orig; resetProofCache() })
	resetProofCache()
}

// WDY-2389: the rescue path exists for a device too far behind to accept our
// certificate, which is usually offline — and so is the host. A proof kept from a
// run that did have a route is what makes the path work then.
func TestFetchProofPacket_FallsBackToCachedProof(t *testing.T) {
	midpoint := time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC)
	stubQuery(t, func() (roughtime.Result, error) {
		return roughtime.Result{Server: "cloudflare", Nonce: []byte("nonce"), RawResponse: []byte("resp"), Midpoint: midpoint}, nil
	})

	fresh, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if ProofFromCache() {
		t.Error("a live query must not report as cached")
	}

	// New process, no route to any server.
	resetProofCache()
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		return roughtime.Result{}, errors.New("network is unreachable")
	}

	cached, result, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("expected the cached proof to be used, got: %v", err)
	}
	if !bytes.Equal(cached, fresh) {
		t.Error("cached packet differs from the one stored")
	}
	if !ProofFromCache() {
		t.Error("ProofFromCache() must report true so the caller can say so")
	}
	if !result.Midpoint.Equal(midpoint) {
		t.Errorf("midpoint = %v, want %v", result.Midpoint, midpoint)
	}
}

func TestFetchProofPacket_NoCacheAndNoNetworkStillErrors(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) {
		return roughtime.Result{}, errors.New("network is unreachable")
	})
	if _, _, err := FetchProofPacket(context.Background()); err == nil {
		t.Fatal("expected an error when there is neither a route nor a cached proof")
	}
}

func TestCacheProof_WritesForALaterRun(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) {
		return roughtime.Result{Server: "int08h", Nonce: []byte("n"), RawResponse: []byte("r"), Midpoint: time.Now()}, nil
	})

	CacheProof(context.Background())

	path, err := proofCachePath()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("CacheProof did not write %s: %v", filepath.Base(path), err)
	}
	// The proof is not a secret, but it sits in ~/.wendy alongside credentials.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache mode = %o, want 0600", perm)
	}
	if _, _, _, err := loadProof(); err != nil {
		t.Errorf("loadProof after CacheProof: %v", err)
	}
}

// A cache we cannot read must not stop a live query from working.
func TestFetchProofPacket_IgnoresCorruptCache(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) {
		return roughtime.Result{Server: "test", Nonce: []byte("n"), RawResponse: []byte("r")}, nil
	})
	path, err := proofCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FetchProofPacket(context.Background()); err != nil {
		t.Fatalf("a corrupt cache must not break a live query: %v", err)
	}
}

func TestLoadProof_RejectsEmptyPacket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path, err := proofCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"packet":"","midpointUnix":1,"server":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadProof(); err == nil {
		t.Fatal("expected an error for an empty cached packet")
	}
}
