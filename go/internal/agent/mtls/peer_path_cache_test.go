package mtls

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerPathCacheClocksAndFixedAge(t *testing.T) {
	wall := time.Now().Round(0)
	mono := time.Now()
	cache := newPeerPathCache()
	cache.clock = func() time.Time { return mono }
	chain := [][]byte{[]byte("exact leaf"), []byte("exact unused issuer")}
	calls := 0
	verify := func(_ [][]byte, now time.Time) (time.Time, error) { calls++; return now.Add(time.Hour), nil }
	check := func(now, effective time.Time, want int) {
		t.Helper()
		if _, err := cache.verify(chain, now, effective, verify); err != nil || calls != want {
			t.Fatalf("verification calls=%d want=%d err=%v", calls, want, err)
		}
	}
	check(wall, wall, 1)
	mono = mono.Add(30 * time.Second)
	check(wall.Add(time.Second), wall.Add(time.Second), 1)
	mono = mono.Add(30 * time.Second)
	check(wall.Add(2*time.Second), wall.Add(2*time.Second), 2) // A hit did not extend the minute.
	check(wall.Add(time.Second), wall.Add(time.Second), 3)     // Wall rollback must miss.
	check(wall.Add(time.Second), wall.Add(time.Second), 4)     // Cannot repopulate below high-water.
	check(wall.Add(3*time.Second), wall.Add(3*time.Second), 5)
	check(wall.Add(4*time.Second), wall.Add(2*time.Second), 6) // Effective-time floor fell backwards.
	mono = mono.Add(-time.Second)
	check(wall.Add(5*time.Second), wall.Add(5*time.Second), 7) // Defensive clock-source rollback.
}

func TestPeerPathCacheExactOwnedChainAndNegativeRetry(t *testing.T) {
	now := time.Now()
	cache := newPeerPathCache()
	calls := 0
	verify := func(_ [][]byte, _ time.Time) (time.Time, error) { calls++; return now.Add(time.Hour), nil }
	a := [][]byte{[]byte("a"), []byte("bc")}
	b := [][]byte{[]byte("ab"), []byte("c")}
	if _, err := cache.verify(a, now, now, verify); err != nil {
		t.Fatal(err)
	}
	a[1][0] = 'x'
	for _, chain := range [][][]byte{a, b, {[]byte("a"), []byte("bc")}} {
		if _, err := cache.verify(chain, now, now, verify); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("chain boundaries, unused bytes or retained ownership lost: calls=%d", calls)
	}
	bad := [][]byte{[]byte("bad")}
	wantErr := errors.New("invalid signature")
	failed := 0
	for range 2 {
		_, err := cache.verify(bad, now, now, func([][]byte, time.Time) (time.Time, error) { failed++; return time.Time{}, wantErr })
		if !errors.Is(err, wantErr) {
			t.Fatal(err)
		}
	}
	if failed != 2 || len(cache.entries) != 3 {
		t.Fatal("negative verification was retained")
	}
}

func TestPeerPathCacheBoundsAndConcurrentUse(t *testing.T) {
	now := time.Now()
	cache := newPeerPathCache()
	cache.maxEntries, cache.maxBytes = 3, 32
	var calls atomic.Int64
	verify := func(_ [][]byte, _ time.Time) (time.Time, error) { calls.Add(1); return now.Add(time.Hour), nil }
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			for range 10 {
				if _, err := cache.verify([][]byte{fmt.Appendf(nil, "%02d", i)}, now, now, verify); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
	if len(cache.entries) > 3 || cache.recency.Len() != len(cache.entries) || cache.bytes > 32 {
		t.Fatalf("bounds exceeded: entries=%d recency=%d bytes=%d", len(cache.entries), cache.recency.Len(), cache.bytes)
	}
	before := calls.Load()
	oversized := [][]byte{make([]byte, 33)}
	for range 2 {
		if _, err := cache.verify(oversized, now, now, verify); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != before+2 || cache.bytes > 32 {
		t.Fatal("oversized chain was cached")
	}
}

func TestPeerPathCacheEvictsLeastRecentlyUsedWithinEachBound(t *testing.T) {
	for _, limits := range []struct {
		name           string
		entries, bytes int
	}{{"entry limit", 2, 1000}, {"byte limit", 100, 18}} {
		t.Run(limits.name, func(t *testing.T) {
			now := time.Now()
			cache := newPeerPathCache()
			cache.maxEntries, cache.maxBytes = limits.entries, limits.bytes
			calls := 0
			verify := func([][]byte, time.Time) (time.Time, error) { calls++; return now.Add(time.Hour), nil }
			for _, value := range []string{"a", "b", "a", "c", "a"} {
				if _, err := cache.verify([][]byte{[]byte(value)}, now, now, verify); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 3 || len(cache.entries) != 2 || cache.bytes != 18 {
				t.Fatalf("LRU/bounds: calls=%d entries=%d bytes=%d", calls, len(cache.entries), cache.bytes)
			}
			if _, err := cache.verify([][]byte{[]byte("b")}, now, now, verify); err != nil || calls != 4 {
				t.Fatalf("least-recent entry was not evicted: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestPeerPathCacheOlderVerificationCannotRepopulateAfterClockAdvance(t *testing.T) {
	now := time.Now()
	cache := newPeerPathCache()
	started, finish := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := cache.verify([][]byte{[]byte("old")}, now, now, func([][]byte, time.Time) (time.Time, error) {
			close(started)
			<-finish
			return now.Add(time.Hour), nil
		})
		done <- err
	}()
	<-started
	if _, err := cache.verify([][]byte{[]byte("new")}, now.Add(time.Second), now.Add(time.Second), func([][]byte, time.Time) (time.Time, error) { return now.Add(time.Hour), nil }); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	oldKey, _, _ := peerChainKey([][]byte{[]byte("old")}, cache.maxBytes)
	if cache.entries[oldKey] != nil || len(cache.entries) != 1 {
		t.Fatal("stale in-flight verification repopulated after a newer observation")
	}
}

func TestCachedPeerVerifierCurrentPathExpiryAndTrust(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	root, rootKey := expiryTestCertificate(t, "root", now, now.Add(time.Hour), nil, nil, true)
	issuer, issuerKey := expiryTestCertificate(t, "issuer", now, now.Add(20*time.Second), root, rootKey, true)
	leaf, _ := expiryTestCertificate(t, "leaf", now, now.Add(time.Hour), issuer, issuerKey, false)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	cache := newPeerPathCache()
	verify := buildPeerCertificateVerifierWithCache(pool, []*x509.Certificate{root}, nil, time.Time{}, cache)
	chain := [][]byte{leaf.Raw, issuer.Raw}
	for _, at := range []time.Time{now, now.Add(time.Second)} {
		if expires, err := verify(chain, at); err != nil || !expires.Equal(issuer.NotAfter) {
			t.Fatalf("verified expiry=%v err=%v", expires, err)
		}
	}
	if len(cache.entries) != 1 {
		t.Fatal("production verifier did not retain a successful path")
	}
	if _, err := verify(chain, issuer.NotAfter.Add(time.Second)); err == nil {
		t.Fatal("warm cache ignored actual issuer expiry")
	}
	if _, err := verify(chain, leaf.NotAfter.Add(time.Second)); err == nil {
		t.Fatal("warm cache ignored leaf expiry")
	}
	if _, err := verify(chain, now.Add(time.Second)); err != nil || len(cache.entries) != 0 {
		t.Fatalf("policy rejection did not advance wall-clock high-water: entries=%d err=%v", len(cache.entries), err)
	}
	wrongRoot, _ := expiryTestCertificate(t, "replacement root", now, now.Add(time.Hour), nil, nil, true)
	rotated, err := NewPeerCertificateVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: wrongRoot.Raw})), nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated(append(chain, root.Raw), now); err == nil {
		t.Fatal("new verifier inherited an old root's cached path")
	}
	// An omitted or replaced issuer must not use a leaf-only cache key.
	if _, err := verify([][]byte{leaf.Raw, wrongRoot.Raw}, now); err == nil {
		t.Fatal("changed issuer chain reused a prior proof")
	}
}

func TestCachedPeerVerifierFloorDiscontinuity(t *testing.T) {
	floor := time.Now().Truncate(time.Second)
	root, key := expiryTestCertificate(t, "root", floor, floor.Add(time.Hour), nil, nil, true)
	leaf, _ := expiryTestCertificate(t, "leaf", floor.Add(2*time.Hour+10*time.Second), floor.Add(time.Hour), root, key, false)
	pool := x509.NewCertPool()
	pool.AddCert(root)
	cache := newPeerPathCache()
	verify := buildPeerCertificateVerifierWithCache(pool, []*x509.Certificate{root}, nil, floor, cache)
	chain := [][]byte{leaf.Raw}
	if _, err := verify(chain, floor.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(cache.entries) != 1 {
		t.Fatal("clock-floor fixture did not warm the cache")
	}
	// Crossing the floor stops the special NotBefore advance, so effective
	// time falls from leaf.NotBefore to floor+1s despite wall time advancing.
	if _, err := verify(chain, floor.Add(time.Second)); err == nil {
		t.Fatal("warm path bypassed the current NotBefore floor policy")
	}
}
