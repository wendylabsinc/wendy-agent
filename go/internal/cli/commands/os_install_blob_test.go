//go:build darwin || linux || windows

package commands

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// writeBlob writes content to a file named name in a temp dir and returns its path.
func writeBlob(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func zstdCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipArchive(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectBlobKind(t *testing.T) {
	rawImage := bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 256)
	tarBytes := tarArchive(t, "manifest.json", []byte("{}"))

	tests := []struct {
		name     string
		fileName string
		content  []byte
		want     blobKind
	}{
		// Extension-driven classification.
		{"tegraflash ext", "custom.tegraflash", rawImage, blobFlashpack},
		{"flashpack ext", "custom.flashpack", rawImage, blobFlashpack},
		{"flashpack tarball ext", "jetson-agx-thor-dev.flashpack.tar.zst", rawImage, blobFlashpack},
		{"generic tar.zst ext", "custom.tar.zst", rawImage, blobFlashpack},
		{"img ext", "custom.img", tarBytes, blobDiskImage},
		{"raw ext", "custom.raw", rawImage, blobDiskImage},
		{"wic ext", "custom.wic", rawImage, blobDiskImage},
		{"sdimg ext", "custom.sdimg", rawImage, blobDiskImage},
		{"zip ext", "custom.zip", zipArchive(t, "a.img", rawImage), blobDiskImage},
		{"img.gz ext", "custom.img.gz", gzipCompress(t, rawImage), blobDiskImage},
		// .img.zst must win over the generic zstd→tar sniff.
		{"img.zst ext", "custom.img.zst", zstdCompress(t, tarBytes), blobDiskImage},

		// Content sniffing for unknown extensions.
		{"sniff gzip", "custom.blob", gzipCompress(t, rawImage), blobDiskImage},
		{"sniff zip", "custom.blob", zipArchive(t, "a.img", rawImage), blobDiskImage},
		{"sniff zstd tar", "custom.blob", zstdCompress(t, tarBytes), blobFlashpack},
		{"sniff zstd raw", "custom.blob", zstdCompress(t, rawImage), blobDiskImage},
		{"sniff raw", "custom.blob", rawImage, blobDiskImage},
		{"sniff short file", "custom.blob", []byte{0x00}, blobDiskImage},
		// .zst without .tar/.img qualifier falls through to the sniff.
		{"bare zst ext tar", "custom.zst", zstdCompress(t, tarBytes), blobFlashpack},
		{"bare zst ext raw", "custom.zst", zstdCompress(t, rawImage), blobDiskImage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeBlob(t, tt.fileName, tt.content)
			got, err := detectBlobKind(path, tt.fileName)
			if err != nil {
				t.Fatalf("detectBlobKind: %v", err)
			}
			if got != tt.want {
				t.Errorf("detectBlobKind(%s) = %d, want %d", tt.fileName, got, tt.want)
			}
		})
	}
}

// TestDetectBlobKindURLHint verifies the name hint (a URL basename) drives the
// extension check even when the local temp file has no meaningful name.
func TestDetectBlobKindURLHint(t *testing.T) {
	path := writeBlob(t, "wendyos-123456.img", zstdCompress(t, tarArchive(t, "manifest.json", []byte("{}"))))
	got, err := detectBlobKind(path, "thing.flashpack.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	if got != blobFlashpack {
		t.Errorf("want flashpack from URL hint, got %d", got)
	}
}

func TestStreamZstdImage(t *testing.T) {
	raw := bytes.Repeat([]byte("wendyos-image-data"), 1024)
	path := writeBlob(t, "custom.img.zst", zstdCompress(t, raw))

	stream, err := streamZstdImage(path)
	if err != nil {
		t.Fatalf("streamZstdImage: %v", err)
	}
	defer stream.Close()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("decompressed bytes differ: got %d bytes, want %d", len(got), len(raw))
	}
	// A plain (non-seekable) zstd stream can't know its decompressed size and
	// must report compressed-consumption progress instead.
	if stream.uncompressedSize != 0 {
		t.Errorf("uncompressedSize = %d, want 0 for plain zstd", stream.uncompressedSize)
	}
	if stream.compressedRead == nil || stream.compressedSize == 0 {
		t.Error("plain zstd stream should carry compressed-progress info")
	}
}

func TestOpenLocalImageStreamZstd(t *testing.T) {
	raw := bytes.Repeat([]byte{0x42}, 4096)
	path := writeBlob(t, "custom.bin", zstdCompress(t, raw))

	stream, err := openLocalImageStream(path)
	if err != nil {
		t.Fatalf("openLocalImageStream: %v", err)
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Error("zstd image not transparently decompressed")
	}
}

func TestFlashpackIncompatibleFlags(t *testing.T) {
	none := blobInstallOptions{force: true, yesOverwriteInternal: true}
	if bad := none.flashpackIncompatibleFlags(); len(bad) != 0 {
		t.Errorf("force/yes-overwrite-internal should be allowed, got %v", bad)
	}

	all := blobInstallOptions{
		drive:      "/dev/disk4",
		wifi:       wifiCLIOptions{SSID: "net", Password: "pw", Entries: []string{"ssid=x"}, NoWifi: true},
		deviceName: "brave-dolphin",
		preOpts:    preEnrollOptions{mode: preEnrollForced, cloudGRPC: "cloud.example:443"},
	}
	bad := all.flashpackIncompatibleFlags()
	want := []string{"--drive", "--wifi-ssid", "--wifi-password", "--wifi", "--no-wifi", "--device-name", "--pre-enroll", "--cloud-grpc"}
	if len(bad) != len(want) {
		t.Fatalf("got %v, want %v", bad, want)
	}
	for i := range want {
		if bad[i] != want[i] {
			t.Errorf("flag %d = %q, want %q", i, bad[i], want[i])
		}
	}
}

func TestInstallBlobFlagValidation(t *testing.T) {
	// A blob install with manifest-backed flags must fail fast.
	for _, flags := range [][]string{
		{"--device-type", "raspberry-pi-5"},
		{"--version", "1.0.0"},
		{"--nightly"},
		{"--storage", "sd"},
	} {
		args := append([]string{"install", "./custom.img"}, flags...)
		cmd := newOSCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := cmd.Execute(); err == nil {
			t.Errorf("wendy os %v should fail flag validation", args)
		}
	}
}
