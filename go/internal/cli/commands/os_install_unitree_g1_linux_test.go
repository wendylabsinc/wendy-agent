//go:build linux

package commands

import (
	"archive/tar"
	"bytes"
	"context"
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

func TestExtractUnitreeG1Tar(t *testing.T) {
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	const scriptName = "Jetpack_6.2_nx/Linux_for_Tegra/flash_nx_module.sh"
	writeUnitreeG1TarEntry(t, tarWriter, tar.Header{Name: "Jetpack_6.2_nx/Linux_for_Tegra", Typeflag: tar.TypeDir, Mode: 0o755}, nil)
	writeUnitreeG1TarEntry(t, tarWriter, tar.Header{Name: scriptName, Typeflag: tar.TypeReg, Mode: 0o755}, []byte("#!/bin/sh\n"))
	writeUnitreeG1TarEntry(t, tarWriter, tar.Header{
		Name:     "Jetpack_6.2_nx/Linux_for_Tegra/flash-hardlink.sh",
		Typeflag: tar.TypeLink,
		Linkname: scriptName,
	}, nil)
	writeUnitreeG1TarEntry(t, tarWriter, tar.Header{
		Name:     "Jetpack_6.2_nx/Linux_for_Tegra/flash-symlink.sh",
		Typeflag: tar.TypeSymlink,
		Linkname: "flash_nx_module.sh",
	}, nil)
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	if err := extractUnitreeG1Tar(context.Background(), bytes.NewReader(archive.Bytes()), root); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, filepath.FromSlash(scriptName))
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\n" {
		t.Fatalf("script contents = %q", data)
	}
	hardInfo, err := os.Stat(filepath.Join(filepath.Dir(script), "flash-hardlink.sh"))
	if err != nil {
		t.Fatal(err)
	}
	scriptInfo, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(scriptInfo, hardInfo) {
		t.Fatal("hard link does not reference the extracted script")
	}
	linkTarget, err := os.Readlink(filepath.Join(filepath.Dir(script), "flash-symlink.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if linkTarget != "flash_nx_module.sh" {
		t.Fatalf("symlink target = %q", linkTarget)
	}
}

func TestExtractUnitreeG1TarRejectsUnsafeLinks(t *testing.T) {
	tests := []struct {
		name    string
		entries []tar.Header
		want    string
	}{
		{
			name: "absolute symlink",
			entries: []tar.Header{{
				Name:     "Jetpack_6.2_nx/Linux_for_Tegra/unsafe",
				Typeflag: tar.TypeSymlink,
				Linkname: "/etc/passwd",
			}},
			want: "absolute target",
		},
		{
			name: "hardlink without prior regular target",
			entries: []tar.Header{{
				Name:     "Jetpack_6.2_nx/Linux_for_Tegra/unsafe",
				Typeflag: tar.TypeLink,
				Linkname: "Jetpack_6.2_nx/Linux_for_Tegra/missing",
			}},
			want: "prior regular file",
		},
		{
			name: "write through archive symlink",
			entries: []tar.Header{
				{Name: "bundle/link", Typeflag: tar.TypeSymlink, Linkname: "real"},
				{Name: "bundle/link/payload", Typeflag: tar.TypeReg, Mode: 0o600},
			},
			want: "would write through symlink",
		},
		{
			name: "regular file traversal",
			entries: []tar.Header{{
				Name:     "../../outside",
				Typeflag: tar.TypeReg,
				Mode:     0o600,
			}},
			want: "unsafe path",
		},
		{
			name: "special file",
			entries: []tar.Header{{
				Name:     "Jetpack_6.2_nx/dev/mem",
				Typeflag: tar.TypeChar,
			}},
			want: "unsupported entry type",
		},
		{
			name: "duplicate regular file",
			entries: []tar.Header{
				{Name: "duplicate", Typeflag: tar.TypeReg, Mode: 0o600},
				{Name: "duplicate", Typeflag: tar.TypeReg, Mode: 0o600},
			},
			want: "duplicate path",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			for _, header := range tc.entries {
				data := []byte(nil)
				if header.Typeflag == tar.TypeReg {
					data = []byte("x")
				}
				writeUnitreeG1TarEntry(t, writer, header, data)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			err := extractUnitreeG1Tar(context.Background(), bytes.NewReader(archive.Bytes()), t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want %q", err, tc.want)
			}
		})
	}
}

func TestExtractUnitreeG1TarRejectsSymlinkRoot(t *testing.T) {
	parent := t.TempDir()
	realRoot := filepath.Join(parent, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkRoot := filepath.Join(parent, "link")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	err := extractUnitreeG1Tar(context.Background(), bytes.NewReader(nil), symlinkRoot)
	if err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("error = %v; want real-directory rejection", err)
	}
}

func writeUnitreeG1TarEntry(t *testing.T, writer *tar.Writer, header tar.Header, data []byte) {
	t.Helper()
	if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
		header.Size = int64(len(data))
	}
	if err := writer.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
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

func TestHashUnitreeG1File(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(artifact, []byte("unitree-g1"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashUnitreeG1File(artifact, nil)
	if err != nil {
		t.Fatal(err)
	}
	const want = "a3cc1c668e3bab252d11494e9181920dde49e94f62f92f60185f420d21d24a46"
	if digest != want {
		t.Fatalf("digest = %q; want %q", digest, want)
	}
}

func TestValidateUnitreeG1TrustPhrase(t *testing.T) {
	if err := validateUnitreeG1TrustPhrase(unitreeG1TrustPhrase); err != nil {
		t.Fatalf("exact phrase rejected: %v", err)
	}
	for _, value := range []string{"", "unverified unitree lab flash", unitreeG1TrustPhrase + " "} {
		if err := validateUnitreeG1TrustPhrase(value); err == nil {
			t.Fatalf("phrase %q was accepted", value)
		}
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
