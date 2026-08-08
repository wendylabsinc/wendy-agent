package lock

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-containerregistry/pkg/crane"
)

// Resolver resolves a floating image reference (e.g. "python:3.12-slim")
// to its exact digest (e.g. "sha256:9f3a..."). The CLI wires in a real,
// registry-backed resolver (CraneResolver); tests use a fake one.
type Resolver func(ref string) (string, error)

// Resolve fills in a digest for every ref in refs, reusing existing's
// current pin unless forceUpdate names that ref explicitly. sourceHash is
// stamped onto the returned file regardless of which refs changed.
func Resolve(existing *File, sourceHash string, refs []string, forceUpdate map[string]bool, resolver Resolver) (*File, []string, error) {
	result := &File{Version: 1, SourceHash: sourceHash, Images: map[string]string{}}
	if existing != nil {
		for ref, digest := range existing.Images {
			result.Images[ref] = digest
		}
	}

	var resolved []string
	for _, ref := range refs {
		_, have := result.Images[ref]
		if have && !forceUpdate[ref] {
			continue
		}
		digest, err := resolver(ref)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving %q: %w", ref, err)
		}
		result.Images[ref] = digest
		resolved = append(resolved, ref)
	}
	return result, resolved, nil
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
