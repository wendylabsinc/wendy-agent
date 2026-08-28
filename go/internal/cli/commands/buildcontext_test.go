package commands

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestPackBuildContext_HonoursOrderedNegations(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "secret.env", "do not transfer")
	// Dockerignore is last-match-wins. The conservative fingerprint matcher
	// intentionally treats every negation as a force-include, but a transfer
	// matcher doing that would leak this file.
	writeFile(t, dir, ".dockerignore", "!secret.env\nsecret.env\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if contextNames(t, tarBytes)["secret.env"] {
		t.Fatal("a later exclusion must override an earlier negation")
	}
}

func TestPackBuildContext_NestedAllowlistDoesNotIncludeSiblings(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "allowed/keep.txt", "keep")
	writeFile(t, dir, "allowed/secret.env", "do not transfer")
	writeFile(t, dir, ".dockerignore", "*\n!allowed\nallowed/*\n!allowed/keep.txt\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["allowed/keep.txt"] {
		t.Fatal("the nested allowlisted file must be transferred")
	}
	if names["allowed/secret.env"] {
		t.Fatal("re-including a directory for traversal must not re-include an excluded sibling")
	}
}

func TestPackBuildContext_HonoursDoubleStarNegation(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "src/deep/keep.txt", "keep")
	writeFile(t, dir, "src/deep/drop.txt", "drop")
	writeFile(t, dir, ".dockerignore", "**\n!src/**/keep.txt\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["src/deep/keep.txt"] {
		t.Fatal("a ** negation must be able to re-include a nested file")
	}
	if names["src/deep/drop.txt"] {
		t.Fatal("a sibling not matched by the ** negation must stay excluded")
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
	if !contextNames(t, tarBytes)["Dockerfile.generated.dockerignore"] {
		t.Fatal("the selected ignore file must be sent so BuildKit applies the same rules")
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

func TestPackBuildContext_PreservesEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	if err := os.MkdirAll(filepath.Join(dir, "runtime", "empty"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if !contextNames(t, tarBytes)["runtime/empty/"] {
		t.Fatal("empty directories are build inputs and must survive transfer")
	}
}

func TestPackBuildContext_RejectsIncludedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require additional privileges on Windows")
	}
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	writeFile(t, dir, "target.txt", "target")
	if err := os.Symlink("target.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := packBuildContext(dir, df)
	if err == nil || !strings.Contains(err.Error(), "do not support non-regular entry \"link.txt\"") {
		t.Fatalf("got %v, want an actionable unsupported-symlink error", err)
	}
}

func TestPackBuildContext_SkipsIgnoredSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require additional privileges on Windows")
	}
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	writeFile(t, dir, filepath.Base(df), "FROM scratch\n")
	if err := os.Symlink("missing", filepath.Join(dir, "ignored-link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	writeFile(t, dir, ".dockerignore", "ignored-link\n")

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("an ignored symlink should not block packing: %v", err)
	}
	if contextNames(t, tarBytes)["ignored-link"] {
		t.Fatal("an ignored symlink must not be transferred")
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
