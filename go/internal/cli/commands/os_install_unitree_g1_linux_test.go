//go:build linux

package commands

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUnitreeG1PackagesRequiresExactPair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, unitreeG1ImageName), []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUnitreeG1Packages(dir); err == nil || !strings.Contains(err.Error(), unitreeG1FirmwareName) {
		t.Fatalf("missing firmware error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitreeG1FirmwareName), []byte("firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveUnitreeG1Packages(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image != filepath.Join(dir, unitreeG1ImageName) || got.Firmware != filepath.Join(dir, unitreeG1FirmwareName) {
		t.Fatalf("resolved packages = %+v", got)
	}
}

func TestResolveUnitreeG1PackagesRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, unitreeG1ImageName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, unitreeG1FirmwareName), []byte("firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveUnitreeG1Packages(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestValidateUnitreeG1Drive(t *testing.T) {
	valid := drive{
		DevicePath:  "/dev/sdz",
		Name:        "Crucial P310",
		Size:        "1.0 TB",
		SizeBytes:   1_000_000_000_000,
		IsRemovable: true,
		MediaFixed:  true,
	}
	if err := validateUnitreeG1Drive(valid); err != nil {
		t.Fatalf("valid drive rejected: %v", err)
	}

	tests := []struct {
		name string
		edit func(*drive)
		want string
	}{
		{"internal", func(d *drive) { d.IsRemovable = false }, "not an external drive"},
		{"removable media", func(d *drive) { d.MediaFixed = false }, "does not look like a fixed SSD"},
		{"undersized", func(d *drive) { d.SizeBytes = 512_000_000_000; d.Size = "512 GB" }, "at least 1 TB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.edit(&candidate)
			if err := validateUnitreeG1Drive(candidate); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestSafeUnitreeG1ArchivePath(t *testing.T) {
	for _, name := range []string{"Jetpack_6.2_nx/Linux_for_Tegra/flash_nx_module.sh", "dir/file"} {
		if !safeUnitreeG1ArchivePath(name) {
			t.Errorf("safe path rejected: %q", name)
		}
	}
	for _, name := range []string{"", "/etc/passwd", "../escape", "dir/../../escape"} {
		if safeUnitreeG1ArchivePath(name) {
			t.Errorf("unsafe path accepted: %q", name)
		}
	}
}

func TestValidateUnitreeG1ArchiveRejectsAbsoluteSymlink(t *testing.T) {
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name:     "Jetpack_6.2_nx/Linux_for_Tegra/unsafe",
		Typeflag: tar.TypeSymlink,
		Linkname: "/etc/passwd",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	if err := validateUnitreeG1Tar(bytes.NewReader(archive.Bytes())); err == nil || !strings.Contains(err.Error(), "absolute target") {
		t.Fatalf("absolute symlink error = %v", err)
	}
}

func TestFindUnitreeG1FlashScript(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Jetpack_6.2_nx", "Linux_for_Tegra")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "flash_nx_module.sh")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := findUnitreeG1FlashScript(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("script = %q; want %q", got, want)
	}
}

func TestStreamBzip2Image(t *testing.T) {
	encoded := "QlpoOTFBWSZTWWNwbZ0AAAeZgAACIAAioxYAIAAiDINAgGmmgibKNXCXcF4u5IpwoSDG4Ns6"
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), unitreeG1ImageName)
	if err := os.WriteFile(image, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	stream, err := streamBzip2Image(image)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unitree-g1-image" {
		t.Fatalf("decompressed = %q", data)
	}
}

func TestSelectUnitreeG1VersionRejectsUnknown(t *testing.T) {
	if got, err := selectUnitreeG1Version(unitreeG1Version); err != nil || got != unitreeG1Version {
		t.Fatalf("version = %q, err = %v", got, err)
	}
	if _, err := selectUnitreeG1Version("5.1.1"); err == nil {
		t.Fatal("expected unsupported version error")
	}
}
