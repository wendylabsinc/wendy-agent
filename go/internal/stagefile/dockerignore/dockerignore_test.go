package dockerignore

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/stagefile/spec"
)

func TestDeriveAllowlistsGivenPaths(t *testing.T) {
	got := Derive([]string{"app.py", "requirements.txt"})
	want := "*\n!app.py\n!requirements.txt\n"
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
