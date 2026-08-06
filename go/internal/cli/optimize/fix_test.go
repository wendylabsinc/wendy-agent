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

func TestSafeAutoApplyFindingsKeepsOnlyPurelyAdditiveFixes(t *testing.T) {
	findings := []Finding{
		{Analyzer: "apt-install", Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "pip-flags", Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "build-cache", Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "add-copy", Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "node-ci"}, // no Fix since the correction — must stay excluded regardless
		{Analyzer: "release-debug", Fix: &Fix{Op: FixReplaceLine}},
		{Analyzer: "arch-image", Fix: &Fix{Op: FixCreateFile}},
		{Analyzer: "image-hygiene"}, // no Fix
	}
	got := SafeAutoApplyFindings(findings)
	if len(got) != 4 {
		t.Fatalf("got %d safe findings, want 4: %+v", len(got), got)
	}
	for _, f := range got {
		switch f.Analyzer {
		case "apt-install", "pip-flags", "build-cache", "add-copy":
		default:
			t.Fatalf("unexpected analyzer %q in safe-auto-apply set", f.Analyzer)
		}
	}
}

func TestApplyFixesToLinesAppliesInMemoryWithoutTouchingDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Dockerfile")
	original := "FROM python:3.11-slim\nRUN pip install -r requirements.txt\n"
	if err := os.WriteFile(p, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := []string{"FROM python:3.11-slim", "RUN pip install -r requirements.txt"}
	findings := []Finding{{Fix: &Fix{
		Op: FixReplaceLine, File: p, Line: 2,
		Old: "RUN pip install -r requirements.txt",
		New: "RUN pip install --no-cache-dir -r requirements.txt",
	}}}

	got, applied := ApplyFixesToLines(lines, findings)

	if len(applied) != 1 || !applied[0].Applied {
		t.Fatalf("applied = %+v", applied)
	}
	want := []string{"FROM python:3.11-slim", "RUN pip install --no-cache-dir -r requirements.txt"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
	// The disk copy must be untouched — this is the whole point of the in-memory variant.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("Dockerfile on disk changed: %q", string(data))
	}
}

func TestApplyFixesToLinesSkipsStaleFix(t *testing.T) {
	lines := []string{"RUN pip install -r requirements.txt"}
	findings := []Finding{{Fix: &Fix{
		Op: FixReplaceLine, Line: 1,
		Old: "RUN some other line entirely",
		New: "RUN pip install --no-cache-dir -r requirements.txt",
	}}}

	got, applied := ApplyFixesToLines(lines, findings)

	if len(applied) != 1 || applied[0].Applied {
		t.Fatalf("expected a skipped (not-applied) fix, got %+v", applied)
	}
	if got[0] != lines[0] {
		t.Fatalf("line should be unchanged, got %q", got[0])
	}
}

func TestApplyFixesToLinesComposesTwoFixesOnTheSameLine(t *testing.T) {
	// A pip install line is exactly the real-world collision: build-cache
	// wants to insert a mount right after RUN, and pip-flags wants to
	// append --no-cache-dir right after "pip install". Both must land.
	lines := []string{"FROM python:3.11-slim", "RUN pip install -r requirements.txt"}
	findings := []Finding{
		{Analyzer: "build-cache", Fix: &Fix{
			Op: FixReplaceLine, Line: 2,
			Old: "RUN pip install -r requirements.txt",
			New: "RUN --mount=type=cache,target=/root/.cache/pip pip install -r requirements.txt",
		}},
		{Analyzer: "pip-flags", Fix: &Fix{
			Op: FixReplaceLine, Line: 2,
			Old: "RUN pip install -r requirements.txt",
			New: "RUN pip install --no-cache-dir -r requirements.txt",
		}},
	}

	got, applied := ApplyFixesToLines(lines, findings)

	for _, a := range applied {
		if !a.Applied {
			t.Fatalf("expected both fixes to be applied, got %+v", applied)
		}
	}
	want := "RUN --mount=type=cache,target=/root/.cache/pip pip install --no-cache-dir -r requirements.txt"
	if got[1] != want {
		t.Fatalf("got  %q\nwant %q", got[1], want)
	}
}

func TestApplyFixesToLinesIgnoresCreateFileFixes(t *testing.T) {
	lines := []string{"FROM debian:12"}
	findings := []Finding{{Fix: &Fix{Op: FixCreateFile, File: ".dockerignore", New: ".git\n"}}}

	got, applied := ApplyFixesToLines(lines, findings)

	if len(applied) != 0 {
		t.Fatalf("expected FixCreateFile to be skipped entirely, got %+v", applied)
	}
	if got[0] != lines[0] {
		t.Fatalf("lines should be unchanged, got %v", got)
	}
}
