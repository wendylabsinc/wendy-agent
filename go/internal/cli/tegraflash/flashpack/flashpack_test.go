package flashpack

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// buildTarball writes a .tar.zst containing the given files (path → content)
// to a temp file and returns its path.
func buildTarball(t *testing.T, files map[string][]byte) string {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "custom.flashpack.tar.zst")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// validFlashpackFiles builds the minimal file set open() and verifyStage1 accept.
func validFlashpackFiles(t *testing.T) map[string][]byte {
	t.Helper()
	stage1 := []byte("rcm-image-bytes")
	sum := sha256.Sum256(stage1)
	manifest, err := json.Marshal(map[string]any{
		"schema":          SupportedSchema,
		"wendyos_version": "dev",
		"default_membct":  "membct.bct",
		"layout": map[string]string{
			"stage1":          "stage1",
			"flash_workspace": "stage2/out/flash_workspace",
		},
		"files": map[string]FileMeta{
			"stage1/rcm.img": {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(stage1))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"manifest.json":  manifest,
		"stage1/rcm.img": stage1,
	}
}

func TestResolveTarball(t *testing.T) {
	tarball := buildTarball(t, validFlashpackFiles(t))
	dest := filepath.Join(t.TempDir(), "extracted")

	fp, err := ResolveTarball(tarball, dest)
	if err != nil {
		t.Fatalf("ResolveTarball: %v", err)
	}
	if fp.Manifest.WendyOSVersion != "dev" {
		t.Errorf("WendyOSVersion = %q, want dev", fp.Manifest.WendyOSVersion)
	}
	if fp.Root != dest {
		t.Errorf("Root = %q, want %q", fp.Root, dest)
	}
	if _, err := os.Stat(filepath.Join(dest, "stage1", "rcm.img")); err != nil {
		t.Errorf("stage1 file not extracted: %v", err)
	}
}

func TestResolveTarballCorruptStage1(t *testing.T) {
	files := validFlashpackFiles(t)
	files["stage1/rcm.img"] = []byte("tampered-bytes!")
	tarball := buildTarball(t, files)

	_, err := ResolveTarball(tarball, filepath.Join(t.TempDir(), "extracted"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch error, got %v", err)
	}
}

func TestResolveTarballNotAFlashpack(t *testing.T) {
	tarball := buildTarball(t, map[string][]byte{"README.txt": []byte("not a flashpack")})

	_, err := ResolveTarball(tarball, filepath.Join(t.TempDir(), "extracted"))
	if err == nil {
		t.Fatal("want error for tarball without manifest.json")
	}
}

// TestResolveTarballPathTraversal proves extractZstTar rejects tar entries
// that escape the destination dir ("tar slip") — custom tarballs are untrusted.
func TestResolveTarballPathTraversal(t *testing.T) {
	files := validFlashpackFiles(t)
	files["../evil.txt"] = []byte("escaped")
	tarball := buildTarball(t, files)

	parent := t.TempDir()
	_, err := ResolveTarball(tarball, filepath.Join(parent, "extracted"))
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("want unsafe-path error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "evil.txt")); statErr == nil {
		t.Fatal("traversal entry escaped the destination dir")
	}
}

// TestResolveTarballSkipsSymlinks proves symlink entries are never
// materialized, so a later entry can't write through one to escape.
func TestResolveTarballSkipsSymlinks(t *testing.T) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}); err != nil {
		t.Fatal(err)
	}
	for name, content := range validFlashpackFiles(t) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	tarball := filepath.Join(t.TempDir(), "links.flashpack.tar.zst")
	if err := os.WriteFile(tarball, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "extracted")
	if _, err := ResolveTarball(tarball, dest); err != nil {
		t.Fatalf("ResolveTarball: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "escape")); !os.IsNotExist(err) {
		t.Fatal("symlink entry was materialized")
	}
}

func TestResolveTarballNotZstd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.img")
	if err := os.WriteFile(path, []byte("raw disk image bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveTarball(path, filepath.Join(t.TempDir(), "extracted"))
	if err == nil {
		t.Fatal("want error for non-zstd input")
	}
}
