package optimize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyReplaceLineIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(p, []byte("FROM rust:1\nRUN cargo build\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Finding{Fix: &Fix{
		Op: FixReplaceLine, File: p, Line: 2,
		Old: "RUN cargo build",
		New: "RUN --mount=type=cache,target=/root/.cargo cargo build",
	}}

	applied, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !applied[0].Applied {
		t.Fatalf("first apply = %+v", applied)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "FROM rust:1\nRUN --mount=type=cache,target=/root/.cargo cargo build\n" {
		t.Fatalf("file after fix = %q", string(data))
	}

	// Re-running must not apply again.
	applied2, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if applied2[0].Applied {
		t.Fatalf("second apply should be skipped, got %+v", applied2[0])
	}
}

func TestApplyCreateFileSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".dockerignore")
	f := Finding{Fix: &Fix{Op: FixCreateFile, File: p, New: ".git\n"}}

	if _, err := ApplyFixes([]Finding{f}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(p) {
		t.Fatalf("file not created")
	}
	applied, err := ApplyFixes([]Finding{f})
	if err != nil {
		t.Fatal(err)
	}
	if applied[0].Applied {
		t.Fatalf("second create should skip existing file")
	}
}
