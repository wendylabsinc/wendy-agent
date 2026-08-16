// Package dockerignore derives .dockerignore content from the local copy
// paths declared in a Stagefile, rather than asking the author to
// hand-maintain a denylist. Every file that can enter a build is named
// explicitly in a copy.from: local entry, so the allowlist is exact.
package dockerignore

import (
	"path"
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

// isContextRoot reports whether a declared copy path is the build context
// itself — `.`, `./`, `/`, or any spelling that cleans to one of those.
//
// Such a path defeats the allowlist rather than narrowing it. `copy: {paths:
// [.]}` would compile to `*` followed by `!.` and `!./**`, and BuildKit cleans
// `./**` to `**`, so the deny-all is undone on the next line and nothing is
// ignored at all. A repo whose .dockerignore excludes a 4 GB .build directory
// would ship it anyway, and COPY it into the image.
//
// There is also nothing to derive here: a stage that copies the whole context
// has already said "everything", so the only ignore rules that can be right are
// the project's own.
func isContextRoot(raw string) bool {
	switch path.Clean(raw) {
	case ".", "/":
		return true
	}
	return false
}

// Derive returns .dockerignore content that denies everything except the given
// paths, or "" when no allowlist can be expressed — see isContextRoot.
//
// "" means "write no ignore file", not "ignore nothing". The distinction
// matters because BuildKit prefers <dockerfile>.dockerignore over
// .dockerignore: a generated file always wins, so emitting one that ignores
// nothing silently disables the project's own .dockerignore, while emitting
// none leaves it in charge.
func Derive(paths []string) string {
	for _, raw := range paths {
		if isContextRoot(raw) {
			return ""
		}
	}
	lines := []string{"*"}
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		lines = append(lines, "!"+p)
	}
	for _, raw := range paths {
		p := strings.TrimPrefix(raw, "./")
		// Ensure parent dirs are reachable when unignoring nested files.
		for dir := p; ; {
			i := strings.LastIndex(dir, "/")
			if i <= 0 {
				break
			}
			add(dir[:i+1])
			dir = dir[:i]
		}
		add(p)
		// Also allow directory contents when p refers to a directory.
		add(p + "/")
		add(p + "/**")
	}
	return strings.Join(lines, "\n") + "\n"
}
