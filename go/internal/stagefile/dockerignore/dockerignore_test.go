package dockerignore

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

// Derive cannot stat the context, so every allowlisted path also gets the
// directory forms (!p/ and !p/**). For a plain file they are inert: BuildKit's
// ignore-file reader runs filepath.Clean on each pattern, so "!app.py/"
// collapses to a duplicate "!app.py", and "!app.py/**" can never match because
// a regular file has no children.
func TestDeriveAllowlistsGivenPaths(t *testing.T) {
	got := Derive([]string{"app.py", "requirements.txt"})
	want := "*\n" +
		"!app.py\n!app.py/\n!app.py/**\n" +
		"!requirements.txt\n!requirements.txt/\n!requirements.txt/**\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A directory path must bring its contents back, not just the directory entry
// itself: under the deny-all "*" base, "!src" alone does not reliably re-include
// descendants.
func TestDeriveAllowsDirectoryContents(t *testing.T) {
	got := Derive([]string{"src"})
	want := "*\n!src\n!src/\n!src/**\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A nested file is unreachable unless each parent directory is unignored too,
// because "*" excludes the parent and the context walk would prune it.
func TestDeriveUnignoresParentDirsOfNestedPaths(t *testing.T) {
	got := Derive([]string{"src/app/main.go"})
	want := "*\n" +
		"!src/app/\n!src/\n" +
		"!src/app/main.go\n!src/app/main.go/\n!src/app/main.go/**\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// "./x" and "x" name the same context path, so they must not produce two
// entries.
func TestDeriveNormalizesDotSlashAndDeduplicates(t *testing.T) {
	got := Derive([]string{"./app.py", "app.py"})
	want := "*\n!app.py\n!app.py/\n!app.py/**\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDeriveWithNoPathsStillDeniesEverything(t *testing.T) {
	got := Derive(nil)
	want := "*\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLocalPathsCollectsAcrossStagesWithoutDuplicates(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "deps", From: "python:3.12-slim", Copy: []spec.CopyEntry{
			{From: "local", Paths: []string{"requirements.txt"}},
		}},
		{Name: "app", From: "python:3.12-slim", Copy: []spec.CopyEntry{
			{From: "local", Paths: []string{"app.py", "requirements.txt"}},
		}},
	}}
	got := LocalPaths(f)
	want := []string{"requirements.txt", "app.py"}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestLocalPathsIgnoresCrossStageCopies(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "deps", From: "python:3.12-slim"},
		{Name: "app", From: "python:3.12-slim", Copy: []spec.CopyEntry{
			{From: "deps", Paths: []string{"/out"}},
		}},
	}}
	if got := LocalPaths(f); len(got) != 0 {
		t.Fatalf("expected no local paths, got %+v", got)
	}
}

func TestLocalPathsIncludesPipRequirements(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Requirements: "requirements.txt"}},
		}},
	}}
	got := LocalPaths(f)
	if len(got) != 1 || got[0] != "requirements.txt" {
		t.Fatalf("got %+v, want [requirements.txt]", got)
	}
}

func TestLocalPathsPipPackagesOnlyAddsNothing(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "python:3.12-slim", Install: &spec.Install{
			Pip: []spec.PipInstall{{Packages: []string{"flask"}}},
		}},
	}}
	if got := LocalPaths(f); len(got) != 0 {
		t.Fatalf("expected no local paths for packages-only pip install, got %+v", got)
	}
}

func TestLocalPathsIncludesNpmManifestFiles(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "node:20-slim", Install: &spec.Install{Npm: &spec.NpmInstall{}}},
	}}
	got := LocalPaths(f)
	want := []string{"package.json", "package-lock.json"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestLocalPathsIncludesYarnLockForYarnManager(t *testing.T) {
	f := &spec.File{Version: 1, Stages: []spec.Stage{
		{Name: "app", From: "node:20-slim", Install: &spec.Install{Npm: &spec.NpmInstall{Manager: "yarn"}}},
	}}
	got := LocalPaths(f)
	want := []string{"package.json", "yarn.lock"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A stage that copies the whole context cannot be narrowed by an allowlist, and
// the naive attempt is actively harmful: `*` then `!./**` cleans to `*` then
// `!**`, which ignores nothing. Because BuildKit prefers
// <dockerfile>.dockerignore over .dockerignore, emitting that would silently
// override the project's own denylist — so a repo excluding a multi-gigabyte
// .build directory would ship it. "" means "write no file", leaving the
// project's .dockerignore in charge.
func TestDeriveWritesNothingForTheContextRoot(t *testing.T) {
	for _, p := range []string{".", "./", "/", "././."} {
		if got := Derive([]string{p}); got != "" {
			t.Errorf("Derive([%q]) = %q, want \"\" (no ignore file)", p, got)
		}
	}
}

// The root poisons the whole allowlist, not just its own entry: once everything
// is re-included, the narrower entries alongside it cannot take anything back.
func TestDeriveContextRootPoisonsOtherPaths(t *testing.T) {
	if got := Derive([]string{"requirements.txt", "."}); got != "" {
		t.Errorf("Derive with a root path among others = %q, want \"\"", got)
	}
}

// The ordinary case must keep denying everything it did before.
func TestDeriveStillAllowlistsNormalPaths(t *testing.T) {
	if got := Derive([]string{"Sources"}); got != "*\n!Sources\n!Sources/\n!Sources/**\n" {
		t.Errorf("normal allowlist regressed: %q", got)
	}
}
