package optimize

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func addCopyTargetWithFile(t *testing.T, name string, data []byte, dockerfile string) *Target {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dockerfileTargetInDir(t, dir, dockerfile)
}

func tarBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	contents := []byte("hello\n")
	if err := tw.WriteHeader(&tar.Header{Name: "hello.txt", Mode: 0o644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAddCopyFlagsLocalFile(t *testing.T) {
	tg := addCopyTargetWithFile(t, "app.py", []byte("print('hello')\n"), "FROM alpine:3\nADD app.py /app/app.py\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Analyzer != "add-copy" || f.Severity != SeverityInfo {
		t.Fatalf("finding = %+v", f)
	}
	if f.Fix == nil || f.Fix.New != "COPY app.py /app/app.py" {
		t.Fatalf("fix = %+v", f.Fix)
	}
}

func TestAddCopySilentOnURL(t *testing.T) {
	tg := dockerfileTargetInDir(t, t.TempDir(), "FROM alpine:3\nADD https://example.com/app.tar.gz /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopySilentOnLocalArchive(t *testing.T) {
	// Docker recognizes tar archives by content rather than filename.
	tg := addCopyTargetWithFile(t, "bundle", tarBytes(t), "FROM alpine:3\nADD bundle /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopyFlagsLocalZip(t *testing.T) {
	// ZIP files are copied by ADD; they are not one of its auto-extracted tar formats.
	tg := addCopyTargetWithFile(t, "bundle.zip", zipBytes(t), "FROM alpine:3\nADD bundle.zip /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 1 || got[0].Fix == nil {
		t.Fatalf("got %+v, want one fixable finding", got)
	}
}

func TestAddCopySilentOnGitSource(t *testing.T) {
	tg := dockerfileTargetInDir(t, t.TempDir(), "FROM alpine:3\nADD git@github.com:user/repo.git /repo\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopySilentWhenSourceCannotBeInspected(t *testing.T) {
	tg := dockerfileTargetInDir(t, t.TempDir(), "FROM alpine:3\nADD generated-at-build-time /app/\n")
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAddCopyIgnoresNonDockerTarget(t *testing.T) {
	tg := &Target{Name: "app", Kind: KindNativeSwift, Arch: "arm64"}
	got := addCopyAnalyzer{}.Analyze(tg)
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
