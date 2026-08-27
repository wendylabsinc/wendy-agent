package lock

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeResolver(answers map[string]string) Resolver {
	return func(ref string) (string, error) {
		d, ok := answers[ref]
		if !ok {
			return "", errors.New("no fake answer for " + ref)
		}
		return d, nil
	}
}

func TestResolveFillsMissingEntries(t *testing.T) {
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:aaa"})
	result, resolved, err := Resolve(nil, "sha256:src1", []string{"debian:12"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:aaa" {
		t.Fatalf("Images = %+v", result.Images)
	}
	if len(resolved) != 1 || resolved[0] != "debian:12" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolveLeavesExistingEntryUntouchedByDefault(t *testing.T) {
	existing := &File{Version: 1, Images: map[string]string{"debian:12": "sha256:old"}}
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:new"})
	result, resolved, err := Resolve(existing, "sha256:src1", []string{"debian:12"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:old" {
		t.Fatalf("expected the existing pin to survive untouched, got %+v", result.Images)
	}
	if len(resolved) != 0 {
		t.Fatalf("expected nothing re-resolved, got %+v", resolved)
	}
}

func TestResolveForceUpdateOverridesExistingEntry(t *testing.T) {
	existing := &File{Version: 1, Images: map[string]string{"debian:12": "sha256:old"}}
	resolver := fakeResolver(map[string]string{"debian:12": "sha256:new"})
	result, resolved, err := Resolve(existing, "sha256:src1", []string{"debian:12"}, map[string]bool{"debian:12": true}, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images["debian:12"] != "sha256:new" {
		t.Fatalf("expected the pin to be updated, got %+v", result.Images)
	}
	if len(resolved) != 1 || resolved[0] != "debian:12" {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestResolvePrunesUnusedImagePins(t *testing.T) {
	existing := &File{Version: 1, Images: map[string]string{
		"python:old": "sha256:old",
		"python:new": "sha256:current",
	}}
	result, _, err := Resolve(existing, "sha256:src1", []string{"python:new"}, nil, fakeResolver(nil))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(result.Images) != 1 || result.Images["python:new"] != "sha256:current" {
		t.Fatalf("Images = %+v, want only the current ref", result.Images)
	}
}

func TestManagedBaseRevisionRefreshesExistingDigest(t *testing.T) {
	const ref = "python:3.14-slim-trixie"
	existing := &File{
		Images:       map[string]string{ref: "sha256:old"},
		ManagedBases: map[string]ManagedBase{"python": {Ref: ref, Revision: 1}},
	}
	desired := map[string]ManagedBase{"python": {Ref: ref, Revision: 2}}
	force := ManagedBaseRefreshes(existing, desired)
	result, resolved, err := Resolve(existing, "sha256:src", []string{ref}, force, fakeResolver(map[string]string{ref: "sha256:new"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if result.Images[ref] != "sha256:new" || len(resolved) != 1 {
		t.Fatalf("Images = %+v, resolved = %+v", result.Images, resolved)
	}
}

func TestManagedBaseUnchangedRevisionReusesDigest(t *testing.T) {
	const ref = "python:3.14-slim-trixie"
	selected := ManagedBase{Ref: ref, Revision: 2}
	existing := &File{
		Images:       map[string]string{ref: "sha256:current"},
		ManagedBases: map[string]ManagedBase{"python": selected},
	}
	if force := ManagedBaseRefreshes(existing, map[string]ManagedBase{"python": selected}); len(force) != 0 {
		t.Fatalf("force = %+v, want no refresh for an unchanged catalog revision", force)
	}
}

func TestAdoptingManagedBaseRefreshesAnExplicitPin(t *testing.T) {
	const ref = "python:3.14-slim-trixie"
	existing := &File{Images: map[string]string{ref: "sha256:explicit"}}
	desired := map[string]ManagedBase{"python": {Ref: ref, Revision: 1}}
	if force := ManagedBaseRefreshes(existing, desired); !force[ref] {
		t.Fatalf("force = %+v, want the newly managed ref refreshed", force)
	}
}

func TestResolvePropagatesResolverError(t *testing.T) {
	resolver := fakeResolver(map[string]string{})
	if _, _, err := Resolve(nil, "sha256:src1", []string{"debian:12"}, nil, resolver); err == nil {
		t.Fatal("expected an error when the resolver fails, got nil")
	}
}

// TestResolveQueriesEveryRefConcurrently is the reason the parallel
// implementation exists: a Stagefile with N distinct base images used to pay N
// sequential registry round-trips, each with its own 30s timeout, inline in
// every build that had an unpinned ref. The fake resolver here blocks until
// every ref's lookup is in flight, which can only happen if Resolve issues them
// at once. A serial Resolve never gets past the first ref, so the bounded wait
// fails the test rather than hanging the suite.
//
// Sized to maxResolveConcurrency so lowering that cap can never silently turn
// this test into a deadlock.
func TestResolveQueriesEveryRefConcurrently(t *testing.T) {
	refs := make([]string, maxResolveConcurrency)
	for i := range refs {
		refs[i] = fmt.Sprintf("base%02d:1", i)
	}

	var inFlight sync.WaitGroup
	inFlight.Add(len(refs))
	resolver := func(ref string) (string, error) {
		inFlight.Done()
		inFlight.Wait() // released only once every other lookup has started too
		return "sha256:" + ref, nil
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := Resolve(nil, "sha256:src1", refs, nil, resolver)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Resolve did not issue all %d ref lookups concurrently", len(refs))
	}
}

// TestResolveReturnsResolvedRefsInDeclarationOrder constrains the parallel
// implementation: the lockfile-facing resolved list must follow the order the
// refs were declared in the Stagefile, never the order the registry happened to
// answer in. Here the first ref is deliberately the slowest to answer.
func TestResolveReturnsResolvedRefsInDeclarationOrder(t *testing.T) {
	resolver := func(ref string) (string, error) {
		if ref == "slow:1" {
			time.Sleep(50 * time.Millisecond)
		}
		return "sha256:" + ref, nil
	}

	_, resolved, err := Resolve(nil, "sha256:src1", []string{"slow:1", "fast:1"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 2 || resolved[0] != "slow:1" || resolved[1] != "fast:1" {
		t.Fatalf("resolved = %+v, want declaration order [slow:1 fast:1]", resolved)
	}
}

// TestResolveReportsTheFirstFailingRefInDeclarationOrder constrains the parallel
// implementation: when several refs fail, the same broken Stagefile must always
// produce the same error. Reporting whichever goroutine failed first would make
// the message depend on registry timing. Here the second ref fails immediately
// while the first takes its time.
func TestResolveReportsTheFirstFailingRefInDeclarationOrder(t *testing.T) {
	resolver := func(ref string) (string, error) {
		if ref == "slow:1" {
			time.Sleep(50 * time.Millisecond)
		}
		return "", errors.New("registry said no")
	}

	_, _, err := Resolve(nil, "sha256:src1", []string{"slow:1", "fast:1"}, nil, resolver)
	if err == nil {
		t.Fatal("expected an error when every resolver call fails, got nil")
	}
	if !strings.Contains(err.Error(), `"slow:1"`) {
		t.Fatalf("err = %v, want it to name the first-declared failing ref slow:1", err)
	}
}

// TestResolveQueriesADuplicatedRefOnce guards a behavior the serial version got
// for free by writing each digest into the result map as it went: a ref listed
// twice must still cost one lookup and appear once in resolved.
func TestResolveQueriesADuplicatedRefOnce(t *testing.T) {
	var calls atomic.Int32
	resolver := func(ref string) (string, error) {
		calls.Add(1)
		return "sha256:aaa", nil
	}

	_, resolved, err := Resolve(nil, "sha256:src1", []string{"debian:12", "debian:12"}, nil, resolver)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resolver called %d times for one distinct ref, want 1", got)
	}
	if len(resolved) != 1 || resolved[0] != "debian:12" {
		t.Fatalf("resolved = %+v, want [debian:12]", resolved)
	}
}

// TestMemoizeResolvesEachRefOnceAcrossConcurrentCallers is the cross-service
// dedup: every service in a compose project on the same base image would
// otherwise issue its own identical registry lookup, and now that those
// compiles run concurrently they would issue them simultaneously. The inner
// resolver blocks until every caller has arrived, so a memoization that only
// caches completed results (rather than making later callers wait on the first
// in-flight lookup) still fails this test.
func TestMemoizeResolvesEachRefOnceAcrossConcurrentCallers(t *testing.T) {
	const callers = 8

	var calls atomic.Int32
	arrived := make(chan struct{}, callers)
	release := make(chan struct{})
	inner := func(ref string) (string, error) {
		calls.Add(1)
		<-release
		return "sha256:aaa", nil
	}
	memoized := Memoize(inner)

	digests := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			arrived <- struct{}{}
			digests[i], errs[i] = memoized("python:3.11-slim")
		})
	}
	for range callers {
		<-arrived
	}
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("inner resolver called %d times, want 1 for %d concurrent callers of the same ref", got, callers)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if digests[i] != "sha256:aaa" {
			t.Fatalf("caller %d got digest %q, want every caller to see the one resolved digest", i, digests[i])
		}
	}
}

// TestMemoizeDoesNotCacheFailures keeps a transient registry error from
// poisoning a ref for the rest of the process: a `wendy watch` session that hit
// one network blip must be able to resolve the same ref on the next rebuild.
func TestMemoizeDoesNotCacheFailures(t *testing.T) {
	var calls atomic.Int32
	inner := func(ref string) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("temporary registry failure")
		}
		return "sha256:aaa", nil
	}
	memoized := Memoize(inner)

	if _, err := memoized("debian:12"); err == nil {
		t.Fatal("expected the first call to surface the resolver error")
	}
	digest, err := memoized("debian:12")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if digest != "sha256:aaa" {
		t.Fatalf("digest = %q, want the retry to reach the resolver and succeed", digest)
	}
}
