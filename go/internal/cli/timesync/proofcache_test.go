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

// stubQuery replaces the Roughtime query for the duration of a test, in a home
// directory of its own so the on-disk cache starts empty.
func stubQuery(t *testing.T, fn func() (roughtime.Result, error)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	orig := roughtimeQueryFn
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		return fn()
	}
	t.Cleanup(func() { roughtimeQueryFn = orig; resetProofCache() })
	resetProofCache()
}

func liveResult() roughtime.Result {
	return roughtime.Result{
		Server:      "cloudflare",
		Nonce:       []byte("nonce"),
		RawResponse: []byte("resp"),
		Midpoint:    time.Date(2026, 8, 11, 3, 0, 0, 0, time.UTC),
		Radius:      2 * time.Second,
	}
}

// WDY-2389: the rescue path exists for a device too far behind to accept our
// certificate, which is usually offline — and so is the host. A proof kept from a
// run that did have a route is what makes the path work then.
func TestFetchProofPacket_FallsBackToCachedProof(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) { return liveResult(), nil })

	fresh, _, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if cached, _ := ProofFromCache(); cached {
		t.Error("a live query must not report as cached")
	}

	// New process, no route to any server.
	resetProofCache()
	roughtimeQueryFn = func(_ context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		return roughtime.Result{}, errors.New("network is unreachable")
	}

	pkt, result, err := FetchProofPacket(context.Background())
	if err != nil {
		t.Fatalf("expected the cached proof to be used, got: %v", err)
	}
	// The datagram is rebuilt from the cached fields rather than stored verbatim,
	// so this also pins that the round trip reproduces it byte for byte.
	if !bytes.Equal(pkt, fresh) {
		t.Error("packet rebuilt from cache differs from the live one")
	}
	cached, age := ProofFromCache()
	if !cached {
		t.Error("ProofFromCache() must report true so the caller can say so")
	}
	if age < 0 || age > time.Minute {
		t.Errorf("age = %v, want the time since it was stored", age)
	}
	// Radius has to survive: the CLI prints it, and a zero would advertise a
	// months-old proof as accurate to the millisecond.
	if want := liveResult(); !result.Midpoint.Equal(want.Midpoint) || result.Radius != want.Radius {
		t.Errorf("result = %v ± %v, want %v ± %v",
			result.Midpoint, result.Radius, want.Midpoint, want.Radius)
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

// A caller that abandoned the operation does not want an older time relayed on
// its behalf.
func TestFetchProofPacket_DoesNotFallBackWhenCallerCancels(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) { return liveResult(), nil })
	if _, _, err := FetchProofPacket(context.Background()); err != nil {
		t.Fatalf("seeding the cache: %v", err)
	}

	resetProofCache()
	roughtimeQueryFn = func(ctx context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		return roughtime.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := FetchProofPacket(ctx); err == nil {
		t.Fatal("a cancelled query must not silently fall back to the cached proof")
	}
}

// The rescue runs under autoSyncTimeAndRetry's 5s budget against servers that do
// not answer, so "no route" presents as that budget running out. Refusing the
// cache then disables the fallback in the one scenario it exists for — which is
// what happened on hardware before this case was pinned.
func TestFetchProofPacket_FallsBackWhenOurOwnDeadlineExpires(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) { return liveResult(), nil })
	if _, _, err := FetchProofPacket(context.Background()); err != nil {
		t.Fatalf("seeding the cache: %v", err)
	}

	resetProofCache()
	roughtimeQueryFn = func(ctx context.Context, _ []roughtime.Server) (roughtime.Result, error) {
		<-ctx.Done()
		return roughtime.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, result, err := FetchProofPacket(ctx)
	if err != nil {
		t.Fatalf("expected the cached proof to be used, got: %v", err)
	}
	if !result.Midpoint.Equal(liveResult().Midpoint) {
		t.Errorf("midpoint = %v, want the cached one", result.Midpoint)
	}
}

// The wire format names the server by index into timesync.Servers, so a cached
// proof naming a server this build does not know cannot be re-encoded — sending
// index 0 would have the device verify against the wrong key.
func TestFetchProofPacket_RejectsCachedProofFromUnknownServer(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) {
		return roughtime.Result{}, errors.New("network is unreachable")
	})
	r := liveResult()
	r.Server = "retired.example.com"
	if err := storeProof(r, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := FetchProofPacket(context.Background()); err == nil {
		t.Fatal("expected a cached proof from an unknown server to be refused")
	}
}

func TestCacheProof_WritesForALaterRun(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) { return liveResult(), nil })

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
	if _, _, err := loadProof(); err != nil {
		t.Errorf("loadProof after CacheProof: %v", err)
	}
	// A failed rename must not leave scratch files next to it.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != proofCacheFile {
			t.Errorf("unexpected leftover file %q in the config dir", e.Name())
		}
	}
}

// A cache we cannot read must not stop a live query from working.
func TestFetchProofPacket_IgnoresCorruptCache(t *testing.T) {
	stubQuery(t, func() (roughtime.Result, error) { return liveResult(), nil })
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

// A proof missing the fields needed to rebuild the datagram must be an error, not
// an empty packet relayed as if it were a time.
func TestLoadProof_RejectsIncompleteProof(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	path, err := proofCachePath()
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"no nonce":    `{"server":"cloudflare","rawResponse":"cmVzcA==","midpointMs":1}`,
		"no response": `{"server":"cloudflare","nonce":"bm9uY2U=","midpointMs":1}`,
		"no server":   `{"nonce":"bm9uY2U=","rawResponse":"cmVzcA==","midpointMs":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadProof(); err == nil {
				t.Fatal("expected an error for an incomplete cached proof")
			}
		})
	}
}
