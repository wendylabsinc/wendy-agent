// Package stagefile is the library facade for the Stagefile compiler — a
// YAML build descriptor (build.stagefile.yaml) that compiles to a real
// Dockerfile with structural safety guarantees a hand-written Dockerfile
// doesn't get by default (lockfile digest-pinning, shell-safe quoting, no
// raw-shell escape hatch). It exposes a single entry point, CompileFile.
// Vendored from github.com/joannisorlandos/stagefile (same author) so
// wendy build/wendy run has no external dependency on that private repo.
package stagefile

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/codegen"
	dockerignorepkg "github.com/wendylabsinc/wendy/go/internal/stagefile/dockerignore"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/lock"
	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// CompileFile reads build.stagefile.yaml from dir, resolves any missing
// lockfile image refs against a live registry (existing pins are never
// touched — only an explicit re-lock changes them), writes/updates
// build.stagefile.lock.yaml in dir, and returns the compiled Dockerfile
// text and the derived .dockerignore text.
func CompileFile(dir, platform string) (dockerfile, dockerignore string, err error) {
	return compileFile(dir, platform, lock.CraneResolver)
}

// compileFile is the resolver-injectable implementation behind
// CompileFile, allowing tests to exercise it with a fake resolver instead
// of a live registry.
func compileFile(dir, platform string, resolver lock.Resolver) (dockerfile, dockerignore string, err error) {
	sourcePath := filepath.Join(dir, "build.stagefile.yaml")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", "", fmt.Errorf("reading %s: %w", sourcePath, err)
	}
	f, err := spec.Parse(raw)
	if err != nil {
		return "", "", err
	}

	lockPath := filepath.Join(dir, "build.stagefile.lock.yaml")
	existing, err := lock.Load(lockPath)
	if err != nil {
		return "", "", err
	}
	updated, _, err := lock.Resolve(existing, spec.SourceHash(raw), imageRefs(f), nil, resolver)
	if err != nil {
		return "", "", err
	}
	if err := updated.Save(lockPath); err != nil {
		return "", "", err
	}

	dockerfile, err = codegen.Generate(f, updated.Images, platform)
	if err != nil {
		return "", "", err
	}
	dockerignore = dockerignorepkg.Derive(dockerignorepkg.LocalPaths(f))
	return dockerfile, dockerignore, nil
}

// imageRefs collects every distinct from: value across f's stages, in
// file order, without duplicates. Stages that opt out of digest pinning
// (pin: false — local-only images with no registry digest) are skipped so
// the resolver never tries to look them up.
func imageRefs(f *spec.File) []string {
	seen := map[string]bool{}
	var refs []string
	for _, s := range f.Stages {
		if s.Pin != nil && !*s.Pin {
			continue
		}
		if !seen[s.From] {
			seen[s.From] = true
			refs = append(refs, s.From)
		}
	}
	return refs
}
