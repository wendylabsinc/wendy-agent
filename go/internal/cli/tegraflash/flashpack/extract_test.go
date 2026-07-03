package flashpack

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// writeZstTar writes entries (name→contents) as a .tar.zst at path.
func writeZstTar(t *testing.T, path string, entries []tar.Header, bodies []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for i, h := range entries {
		hdr := h
		hdr.Size = int64(len(bodies[i]))
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(bodies[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractZstTar_Normal(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "pack.tar.zst")
	writeZstTar(t,
		tarball,
		[]tar.Header{
			{Name: "manifest.json", Mode: 0o644, Typeflag: tar.TypeReg},
			{Name: "stage1/br.bct", Mode: 0o644, Typeflag: tar.TypeReg},
		},
		[]string{`{"schema":1}`, "bct-bytes"},
	)

	dest := filepath.Join(dir, "extracted")
	if err := extractZstTar(tarball, dest); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "stage1", "br.bct"))
	if err != nil {
		t.Fatalf("expected extracted file: %v", err)
	}
	if string(got) != "bct-bytes" {
		t.Fatalf("content = %q, want %q", got, "bct-bytes")
	}
}

func TestExtractZstTar_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "evil.tar.zst")
	writeZstTar(t,
		tarball,
		[]tar.Header{{Name: "../escape.txt", Mode: 0o644, Typeflag: tar.TypeReg}},
		[]string{"pwned"},
	)

	dest := filepath.Join(dir, "extracted")
	if err := extractZstTar(tarball, dest); err == nil {
		t.Fatal("expected traversal entry to be rejected")
	}
	// The escape target must not have been written outside dest.
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("path traversal wrote outside dest: %v", err)
	}
}

func TestExtractZstTar_SkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	tarball := filepath.Join(dir, "link.tar.zst")
	writeZstTar(t,
		tarball,
		[]tar.Header{{Name: "evil-link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		[]string{""},
	)

	dest := filepath.Join(dir, "extracted")
	if err := extractZstTar(tarball, dest); err != nil {
		t.Fatalf("extract failed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "evil-link")); !os.IsNotExist(err) {
		t.Fatal("symlink entry should have been skipped, not created")
	}
}
