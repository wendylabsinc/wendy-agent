package lock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/gpu"
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
// is normally resolved only when it has no pin. A managed catalog revision can
// force one refresh, but a running CLI cannot change its compiled-in catalog,
// so the process-level answer is still stable. Failures are deliberately NOT cached —
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
// stamped onto the returned file regardless of which refs changed. Any
// download pins already recorded carry forward untouched; ResolveDownloads
// updates those.
func Resolve(existing *File, sourceHash string, refs []string, forceUpdate map[string]bool, resolver Resolver) (*File, []string, error) {
	result := &File{Version: 1, SourceHash: sourceHash, Images: map[string]string{}}
	if existing != nil {
		// Carry only refs the current Stagefile still declares. This matters for
		// managed channels because moving (say) Python to a new release line
		// should replace the old catalog ref instead of accumulating it forever.
		for _, ref := range refs {
			if digest, ok := existing.Images[ref]; ok {
				result.Images[ref] = digest
			}
		}
		if len(existing.ManagedBases) > 0 {
			result.ManagedBases = map[string]ManagedBase{}
			maps.Copy(result.ManagedBases, existing.ManagedBases)
		}
		if len(existing.Downloads) > 0 {
			result.Downloads = map[string]string{}
			maps.Copy(result.Downloads, existing.Downloads)
		}
		if len(existing.CUDA) > 0 {
			result.CUDA = map[string]gpu.Profile{}
			maps.Copy(result.CUDA, existing.CUDA)
		}
	}
	pending, err := pinAll(result.Images, refs, forceUpdate, resolver)
	if err != nil {
		return nil, nil, err
	}
	return result, pending, nil
}

// ResolveDownloads fills in a sha256 for every URL in urls, reusing f's
// current pin unless forceUpdate names that URL explicitly. It shares
// pinAll with image resolution so "reuse the existing pin unless forced"
// has one implementation and cannot mean two things.
func (f *File) ResolveDownloads(urls []string, forceUpdate map[string]bool, hasher Hasher) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	if f.Downloads == nil {
		f.Downloads = map[string]string{}
	}
	return pinAll(f.Downloads, urls, forceUpdate, func(url string) (string, error) { return hasher(url) })
}

// pinAll resolves every ref in refs that current has no pin for (or that
// forceUpdate names), writing the results into current and returning the refs
// it actually resolved, in declaration order.
func pinAll(current map[string]string, refs []string, forceUpdate map[string]bool, lookup func(string) (string, error)) ([]string, error) {
	// Collect the refs that actually need a lookup, in declaration order and
	// without duplicates. Doing this up front (rather than writing digests into
	// current as we go) is what keeps both the returned list and the
	// error we report keyed to the source file instead of to whichever lookup
	// happened to finish first.
	var pending []string
	seen := map[string]bool{}
	for _, ref := range refs {
		if _, have := current[ref]; have && !forceUpdate[ref] {
			continue
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		pending = append(pending, ref)
	}
	if len(pending) == 0 {
		return nil, nil
	}

	// Each lookup is an independent network round-trip, so they run
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
			digests[i], errs[i] = lookup(ref)
		})
	}
	wg.Wait()

	// Report the first failure in declaration order, so the same broken
	// Stagefile always produces the same error rather than one that depends on
	// registry timing.
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", pending[i], err)
		}
	}
	for i, ref := range pending {
		current[ref] = digests[i]
	}
	return pending, nil
}

// Hasher returns the sha256 of the bytes a URL serves, as "sha256:<hex>".
// It is what pins a download whose Stagefile declares no sha256 of its own;
// the CLI wires in HTTPHasher, tests use a fake.
type Hasher func(url string) (string, error)

// downloadHeaderTimeout bounds how long a server may take to send response
// headers. There is deliberately no timeout on the body: this exists to fetch
// files like a 310 MB ONNX voice model, and a total deadline generous enough
// for that on a slow link is not a timeout, while one that isn't would fail
// exactly the case the feature was built for. A server that accepts the
// connection and then says nothing is still bounded here, and a body that
// stalls mid-stream surfaces as a read error from the transport rather than
// hanging forever.
const downloadHeaderTimeout = 30 * time.Second

// HTTPHasher is a Hasher backed by a real HTTP GET. Like CraneResolver it is
// intentionally not covered by a network-dependent test; the tests drive it
// against an httptest.Server instead.
func HTTPHasher(url string) (string, error) {
	client := &http.Client{Transport: &http.Transport{
		ResponseHeaderTimeout: downloadHeaderTimeout,
		Proxy:                 http.ProxyFromEnvironment,
	}}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetching %q: %w", url, err)
	}
	defer resp.Body.Close()
	// A 404 page hashes just as well as a model does. Checking the status
	// is what stops "pinned successfully" from meaning "pinned the error".
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("fetching %q: %s", url, resp.Status)
	}
	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Errorf("reading %q: %w", url, err)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
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
