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
