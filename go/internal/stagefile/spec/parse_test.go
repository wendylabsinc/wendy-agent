package spec

import (
	"strings"
	"testing"
)

func TestParseMinimalStage(t *testing.T) {
	src := []byte(`
version: 1
stages:
  - name: app
    from: debian:12
    entrypoint:
      exec: ["/bin/app"]
`)
	f, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Version != 1 {
		t.Fatalf("Version = %d, want 1", f.Version)
	}
	if len(f.Stages) != 1 {
		t.Fatalf("got %d stages, want 1", len(f.Stages))
	}
	s := f.Stages[0]
	if s.Name != "app" || s.From != "debian:12" {
		t.Fatalf("stage = %+v", s)
	}
	if s.Entrypoint == nil || len(s.Entrypoint.Exec) != 1 || s.Entrypoint.Exec[0] != "/bin/app" {
		t.Fatalf("entrypoint = %+v", s.Entrypoint)
	}
}

func TestParseResolvesManagedBase(t *testing.T) {
	f, err := Parse([]byte("version: 1\nstages:\n  - name: app\n    base: python\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	stage := f.Stages[0]
	if stage.Base != "python" || stage.From != managedBaseCatalog["python"].Ref {
		t.Fatalf("stage = %+v, want managed Python ref %q", stage, managedBaseCatalog["python"].Ref)
	}
	bases := f.ManagedBases()
	if len(bases) != 1 || bases[0] != managedBaseCatalog["python"] {
		t.Fatalf("ManagedBases = %+v", bases)
	}
	if err := f.Validate(); err != nil {
		t.Fatalf("second Validate must be idempotent: %v", err)
	}
}

func TestParseRejectsBaseAndFromTogether(t *testing.T) {
	_, err := Parse([]byte("version: 1\nstages:\n  - name: app\n    base: python\n    from: python:3.14-slim\n"))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want base/from exclusivity error", err)
	}
}

func TestParseRejectsUnknownManagedBase(t *testing.T) {
	_, err := Parse([]byte("version: 1\nstages:\n  - name: app\n    base: ruby\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown base") || !strings.Contains(err.Error(), "python") {
		t.Fatalf("err = %v, want unknown base error with available choices", err)
	}
}

func TestParseRejectsUnpinnedManagedBase(t *testing.T) {
	_, err := Parse([]byte("version: 1\nstages:\n  - name: app\n    base: python\n    pin: false\n"))
	if err == nil || !strings.Contains(err.Error(), "cannot set pin: false") {
		t.Fatalf("err = %v, want managed base pinning error", err)
	}
}

func TestParseRejectsUnsupportedVersion(t *testing.T) {
	src := []byte("version: 2\nstages:\n  - name: app\n    from: debian:12\n")
	if _, err := Parse(src); err == nil {
		t.Fatal("expected an error for version 2, got nil")
	}
}

func TestParseRejectsEmptyStages(t *testing.T) {
	src := []byte("version: 1\nstages: []\n")
	if _, err := Parse(src); err == nil {
		t.Fatal("expected an error for zero stages, got nil")
	}
}

func TestParsePipBuildPackages(t *testing.T) {
	f, err := Parse([]byte(`
version: 1
stages:
  - name: app
    from: python:3.12
    install:
      pip:
        - packages: [native]
          buildPackages: [gcc, python3-dev]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := f.Stages[0].Install.Pip[0].BuildPackages
	if len(got) != 2 || got[0] != "gcc" || got[1] != "python3-dev" {
		t.Fatalf("buildPackages = %v", got)
	}
}

func TestSourceHashIsStableAndPrefixed(t *testing.T) {
	h1 := SourceHash([]byte("hello"))
	h2 := SourceHash([]byte("hello"))
	if h1 != h2 {
		t.Fatalf("hash not stable: %q vs %q", h1, h2)
	}
	if h1[:7] != "sha256:" {
		t.Fatalf("hash missing sha256: prefix: %q", h1)
	}
	if SourceHash([]byte("other")) == h1 {
		t.Fatal("different input produced the same hash")
	}
}

var benchmarkParsedFile *File

func BenchmarkParseExplicitFrom(b *testing.B) {
	src := []byte("version: 1\nstages:\n  - name: app\n    from: python:3.14-slim-trixie\n")
	b.ReportAllocs()
	for b.Loop() {
		var err error
		benchmarkParsedFile, err = Parse(src)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseManagedBase(b *testing.B) {
	src := []byte("version: 1\nstages:\n  - name: app\n    base: python\n")
	b.ReportAllocs()
	for b.Loop() {
		var err error
		benchmarkParsedFile, err = Parse(src)
		if err != nil {
			b.Fatal(err)
		}
	}
}
