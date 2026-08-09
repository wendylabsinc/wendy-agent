// Package dockerignore derives .dockerignore content from the local copy
// paths declared in a Stagefile, rather than asking the author to
// hand-maintain a denylist. Every file that can enter a build is named
// explicitly in a copy.from: local entry, so the allowlist is exact.
package dockerignore

import (
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// LocalPaths collects every path referenced by a copy.from: local entry
// across every stage of f, in file order, without duplicates.
func LocalPaths(f *spec.File) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for _, s := range f.Stages {
		for _, c := range s.Copy {
			if c.From != "local" {
				continue
			}
			for _, p := range c.Paths {
				add(p)
			}
		}
		for _, p := range s.Install.LocalFiles() {
			add(p)
		}
	}
	return paths
}

// Derive returns .dockerignore content that denies everything except the
// given paths.
func Derive(paths []string) string {
	lines := []string{"*"}
	for _, p := range paths {
		lines = append(lines, "!"+p)
	}
	return strings.Join(lines, "\n") + "\n"
}
