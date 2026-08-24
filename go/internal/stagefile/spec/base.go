package spec

import (
	"fmt"
	"slices"
	"strings"
)

// ManagedBase is one Wendy-maintained base-image channel. Ref intentionally
// follows a language/distribution minor line rather than a patch release: the
// lockfile supplies reproducibility, while Revision tells the compiler when a
// Wendy release deliberately refreshes that channel's digest.
type ManagedBase struct {
	Name     string
	Ref      string
	Revision int
}

// managedBaseCatalog is deliberately small and opinionated. These are official
// images on maintained release lines, with glibc-based slim runtimes preferred
// over Alpine so native wheels and dynamically linked application binaries do
// not unexpectedly cross the musl boundary.
//
// Bump Revision whenever a channel should pick up a rebuilt tag at a new digest,
// even if Ref itself does not change. Existing projects then get a visible
// lockfile update exactly once, rather than floating on every build.
var managedBaseCatalog = map[string]ManagedBase{
	"debian":        {Name: "debian", Ref: "debian:trixie-slim", Revision: 1},
	"go":            {Name: "go", Ref: "golang:1.27-trixie", Revision: 1},
	"node":          {Name: "node", Ref: "node:24-bookworm-slim", Revision: 1},
	"python":        {Name: "python", Ref: "python:3.14-slim-trixie", Revision: 1},
	"rust":          {Name: "rust", Ref: "rust:1.98-slim-trixie", Revision: 1},
	"swift":         {Name: "swift", Ref: "swift:6.3-noble", Revision: 1},
	"swift-runtime": {Name: "swift-runtime", Ref: "swift:6.3-noble-slim", Revision: 1},
}

// ManagedBaseNames returns the accepted base: values in stable display order.
func ManagedBaseNames() []string {
	names := make([]string, 0, len(managedBaseCatalog))
	for name := range managedBaseCatalog {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// resolveManagedBases validates and resolves every base: selection in-place.
// It is idempotent so callers may validate an already parsed File again.
func (f *File) resolveManagedBases() error {
	for i := range f.Stages {
		s := &f.Stages[i]
		if s.Base == "" {
			continue
		}
		selected, ok := managedBaseCatalog[s.Base]
		if !ok {
			return fmt.Errorf("stage %q: unknown base %q (choose one of %s, or use from: for an explicit image)", s.Name, s.Base, strings.Join(ManagedBaseNames(), ", "))
		}
		if !s.managedBaseResolved {
			if s.From != "" {
				return fmt.Errorf("stage %q: base and from are mutually exclusive", s.Name)
			}
			s.managedBaseResolved = true
			s.From = selected.Ref
			continue
		}
		if s.From != selected.Ref {
			return fmt.Errorf("stage %q: managed base %q was modified after validation", s.Name, s.Base)
		}
	}
	return nil
}

// ManagedBases returns the managed channels used by f, deduplicated by name in
// first-use order. File validation must run first so each selection is resolved.
func (f *File) ManagedBases() []ManagedBase {
	seen := map[string]bool{}
	var bases []ManagedBase
	for i := range f.Stages {
		s := &f.Stages[i]
		if !s.managedBaseResolved {
			continue
		}
		selected := managedBaseCatalog[s.Base]
		if seen[selected.Name] {
			continue
		}
		seen[selected.Name] = true
		bases = append(bases, selected)
	}
	return bases
}
