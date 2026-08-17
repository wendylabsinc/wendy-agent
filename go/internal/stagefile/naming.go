package stagefile

import (
	"os"
	"regexp"
	"sort"
	"strings"
)

// SourceName is the conventional Stagefile source filename, matching the
// standalone stagefile tool's own CLI default. It is the canonical member of
// the "<variant>.stagefile.yaml" family and the one every build-file picker
// prefers when a project carries several.
const SourceName = "build.stagefile.yaml"

const (
	sourceSuffix = ".stagefile.yaml"
	lockSuffix   = ".stagefile.lock.yaml"
)

// sourceNameRe matches a Stagefile source: a variant token followed by
// ".stagefile.yaml".
//
// The variant token deliberately excludes dots. That single restriction is what
// keeps "build.stagefile.lock.yaml" out of the family for free — its would-be
// variant token is "build.stagefile.lock", which contains dots and so cannot
// match. Without it, every project's lockfile would be detected as a rival
// build file sitting next to its own source.
var sourceNameRe = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\.stagefile\.yaml$`)

// IsSourceName reports whether name is a Stagefile source filename —
// "build.stagefile.yaml" or a variant of it such as "prod.stagefile.yaml".
// name must be a bare filename; a path is never a source name.
func IsSourceName(name string) bool {
	_, ok := SourceVariant(name)
	return ok
}

// SourceVariant returns the variant token of a Stagefile source filename:
// "build" for the canonical SourceName, "prod" for "prod.stagefile.yaml". ok is
// false when name is not a Stagefile source at all.
func SourceVariant(name string) (variant string, ok bool) {
	m := sourceNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// IsCanonicalSourceName reports whether name is exactly SourceName. Callers use
// this to keep the canonical source on its historical artifact filenames while
// variants get suffixed ones.
func IsCanonicalSourceName(name string) bool {
	return name == SourceName
}

// LockName returns the lockfile filename paired with a Stagefile source:
// "build.stagefile.yaml" → "build.stagefile.lock.yaml". Each variant keeps its
// own lockfile, because two variants of one project routinely pin different
// base images and sharing one lockfile would make each build re-resolve the
// other's pins. Returns "" if source is not a Stagefile source name.
func LockName(source string) string {
	variant, ok := SourceVariant(source)
	if !ok {
		return ""
	}
	return variant + lockSuffix
}

// IsLockName reports whether name is a Stagefile lockfile — the artifact
// LockName produces for some source in the family.
func IsLockName(name string) bool {
	if !strings.HasSuffix(name, lockSuffix) {
		return false
	}
	// Reuse the source grammar so the variant token is validated identically:
	// "<variant>.stagefile.lock.yaml" is a lockfile iff "<variant>.stagefile.yaml" is a source.
	return IsSourceName(strings.TrimSuffix(name, lockSuffix) + sourceSuffix)
}

// SourceNames returns every Stagefile source in dir, canonical-first and then
// alphabetically, so a picker built from the result leads with the source a
// project without an explicit choice would build. Returns nil when dir holds
// none or cannot be read — "no Stagefiles here" is the answer either way, and
// the real diagnostic belongs to whatever tries to build.
func SourceNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if IsSourceName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if a, b := names[i] == SourceName, names[j] == SourceName; a != b {
			return a
		}
		return names[i] < names[j]
	})
	return names
}
