package spec

import "testing"

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
