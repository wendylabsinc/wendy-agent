package commands

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func timeUnix(sec int64) time.Time { return time.Unix(sec, 0) }

func contextNames(t *testing.T, tarBytes []byte) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("reading context tar: %v", err)
		}
		names[hdr.Name] = true
	}
}

func TestPackBuildContext_HonoursDockerignore(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "keep.txt", "keep")
	writeFile(t, dir, "secret.env", "nope")
	writeFile(t, dir, ".dockerignore", "secret.env\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["keep.txt"] {
		t.Error("keep.txt must be in the context")
	}
	if names["secret.env"] {
		t.Error("secret.env is ignored and must not be shipped to the build host")
	}
}

// The Stagefile-derived allowlist lives in <dockerfile>.dockerignore, which
// BuildKit prefers over .dockerignore for the file passed via -f. Picking the
// wrong one ships a context missing files the build needs.
func TestPackBuildContext_PrefersPerDockerfileIgnore(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile.generated")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "app.py", "x")
	writeFile(t, dir, "notes.md", "x")
	writeFile(t, dir, ".dockerignore", "app.py\n")
	writeFile(t, dir, filepath.Base(df)+".dockerignore", "notes.md\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["app.py"] {
		t.Error("app.py must be present: the per-dockerfile ignore file wins and does not exclude it")
	}
	if names["notes.md"] {
		t.Error("notes.md must be absent: the per-dockerfile ignore file excludes it")
	}
}

func TestPackBuildContext_AlwaysIncludesTheDockerfile(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile.generated")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	// An allowlist-style ignore that would otherwise exclude the build file.
	writeFile(t, dir, filepath.Base(df)+".dockerignore", "*\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if !contextNames(t, tarBytes)["Dockerfile.generated"] {
		t.Fatal("the build file must be sent explicitly, not left to survive the ignore rules")
	}
}

func TestPackBuildContext_PrunesIgnoredDirectories(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, dir, "node_modules"+"/"+"pkg"+"/"+"index.js", "x")
	writeFile(t, dir, ".dockerignore", "node_modules\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if contextNames(t, tarBytes)["node_modules/pkg/index.js"] {
		t.Fatal("an ignored directory must be pruned, not walked into")
	}
}

// Chunk dedup is content-addressed, so a nondeterministic tar would re-send the
// entire context on every build even when nothing changed.
func TestPackBuildContext_Deterministic(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "a.txt", "a")
	writeFile(t, dir, "b.txt", "b")

	first, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	second, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("an unchanged context must pack identically, or every build re-sends every chunk")
	}
}

// mtime is not build input; letting it vary would defeat chunk dedup.
func TestPackBuildContext_IgnoresModTime(t *testing.T) {
	pack := func(t *testing.T, mod int64) []byte {
		t.Helper()
		dir := t.TempDir()
		df := filepath.Join(dir, "Dockerfile")
		writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
		writeFile(t, dir, "a.txt", "a")
		stamp := timeUnix(mod)
		if err := os.Chtimes(filepath.Join(dir, "a.txt"), stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		out, err := packBuildContext(dir, df)
		if err != nil {
			t.Fatalf("packBuildContext: %v", err)
		}
		return out
	}
	if !bytes.Equal(pack(t, 1_000_000_000), pack(t, 1_700_000_000)) {
		t.Fatal("identical content with different mtimes must pack identically")
	}
}
