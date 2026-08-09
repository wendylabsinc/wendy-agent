package lock

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
)

// maxResolveConcurrency bounds how many registry lookups one Resolve issues at
// once. Registry digest lookups are network-bound, not CPU-bound, so this is
// about not opening an unbounded number of connections to a registry (which
// invites rate limiting) rather than about saturating the machine.
const maxResolveConcurrency = 8

// Resolver resolves a floating image reference (e.g. "python:3.12-slim")
// to its exact digest (e.g. "sha256:9f3a..."). The CLI wires in a real,
// registry-backed resolver (CraneResolver); tests use a fake one.
type Resolver func(ref string) (string, error)

// memoEntry is one ref's in-flight-or-finished lookup. done is closed once
// digest/err are written, so a caller that arrives mid-flight waits for the
// first caller's result instead of issuing a second identical lookup.
type memoEntry struct {
	done   chan struct{}
	digest string
	err    error
}

// Memoize returns a Resolver that asks r for any given ref at most once,
// collapsing the duplicate lookups that appear when several Stagefiles compile
// in the same process — every service in a compose project sharing one
// python:3.11-slim base, say. Since those compiles now also run concurrently,
// deduplication has to cover in-flight lookups and not just finished ones, so
// later callers block on the first caller's result.
//
// Caching for the process lifetime matches the lockfile's own semantics: a ref
// is only ever resolved when it has no pin, and once pinned it is never
// re-resolved until an explicit re-lock. Failures are deliberately NOT cached —
// a network blip must not poison the ref for the rest of a long-lived
// `wendy watch` session.
func Memoize(r Resolver) Resolver {
	var mu sync.Mutex
	entries := map[string]*memoEntry{}

	return func(ref string) (string, error) {
		mu.Lock()
		if e, ok := entries[ref]; ok {
			mu.Unlock()
			<-e.done
			return e.digest, e.err
		}
		e := &memoEntry{done: make(chan struct{})}
		entries[ref] = e
		mu.Unlock()

		e.digest, e.err = r(ref)
		close(e.done)

		if e.err != nil {
			mu.Lock()
			// Guard the identity check: a retry that already replaced this
			// entry must not have its own lookup dropped from the map.
			if entries[ref] == e {
				delete(entries, ref)
			}
			mu.Unlock()
		}
		return e.digest, e.err
	}
}

// Resolve fills in a digest for every ref in refs, reusing existing's
// current pin unless forceUpdate names that ref explicitly. sourceHash is
// stamped onto the returned file regardless of which refs changed.
func Resolve(existing *File, sourceHash string, refs []string, forceUpdate map[string]bool, resolver Resolver) (*File, []string, error) {
	result := &File{Version: 1, SourceHash: sourceHash, Images: map[string]string{}}
	if existing != nil {
		maps.Copy(result.Images, existing.Images)
	}

	// Collect the refs that actually need a lookup, in declaration order and
	// without duplicates. Doing this up front (rather than writing digests into
	// result.Images as we go) is what keeps both the returned list and the
	// error we report keyed to the source file instead of to whichever lookup
	// happened to finish first.
	var pending []string
	seen := map[string]bool{}
	for _, ref := range refs {
		if _, have := result.Images[ref]; have && !forceUpdate[ref] {
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		pending = append(pending, ref)
	}
	if len(pending) == 0 {
		return result, nil, nil
	}

	// Each lookup is an independent registry round-trip, so they run
	// concurrently: a Stagefile with N distinct base images used to pay N
	// sequential round-trips — each with its own 30s CraneResolver timeout —
	// inline in every build that had an unpinned ref. Results land in a fixed
	// slot per ref rather than a shared append, so the parallelism cannot
	// reorder anything observable.
	digests := make([]string, len(pending))
	errs := make([]error, len(pending))
	sem := make(chan struct{}, maxResolveConcurrency)
	var wg sync.WaitGroup
	for i, ref := range pending {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			digests[i], errs[i] = resolver(ref)
		})
	}
	wg.Wait()

	// Report the first failure in declaration order, so the same broken
	// Stagefile always produces the same error rather than one that depends on
	// registry timing.
	for i, err := range errs {
		if err != nil {
			return nil, nil, fmt.Errorf("resolving %q: %w", pending[i], err)
		}
	}
	for i, ref := range pending {
		result.Images[ref] = digests[i]
	}
	return result, pending, nil
}

// CraneResolver is a Resolver backed by a real registry lookup. It is
// intentionally not covered by an automated test so the suite never depends
// on network access; tests inject a fake Resolver instead. The timeout
// bounds a hung registry connection: resolution runs inline inside every
// build of a project whose lockfile is missing a pin, and without it a
// stalled connection would hang the build forever.
func CraneResolver(ref string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	digest, err := crane.Digest(ref, crane.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("crane digest %q: %w", ref, err)
	}
	return digest, nil
}
