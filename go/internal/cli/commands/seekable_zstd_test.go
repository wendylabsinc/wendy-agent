package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	seekable "github.com/SaveTheRbtz/zstd-seekable-format-go/pkg"
	"github.com/klauspost/compress/zstd"
)

// encodeSeekable compresses data into the seekable-zstd container using
// frameSize-byte frames, returning the compressed bytes. Test-only helper.
func encodeSeekable(t *testing.T, data []byte, frameSize int) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	defer enc.Close()
	var buf bytes.Buffer
	w, err := seekable.NewWriter(&buf, enc)
	if err != nil {
		t.Fatalf("seekable.NewWriter: %v", err)
	}
	for off := 0; off < len(data); off += frameSize {
		end := off + frameSize
		if end > len(data) {
			end = len(data)
		}
		if _, err := w.Write(data[off:end]); err != nil {
			t.Fatalf("seekable write: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("seekable close: %v", err)
	}
	return buf.Bytes()
}

func TestSeekableImageReadAtAndSize(t *testing.T) {
	data := make([]byte, 10_000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	comp := encodeSeekable(t, data, 1024)

	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("openSeekableZstdFromReader: %v", err)
	}
	defer si.Close()

	if si.Size() != int64(len(data)) {
		t.Fatalf("Size() = %d, want %d", si.Size(), len(data))
	}
	for _, tc := range []struct{ off, n int }{{0, 100}, {1000, 2048}, {5000, 4000}, {9990, 10}} {
		got := make([]byte, tc.n)
		if _, err := si.ReadAt(got, int64(tc.off)); err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d,%d): %v", tc.off, tc.n, err)
		}
		if !bytes.Equal(got, data[tc.off:tc.off+tc.n]) {
			t.Fatalf("ReadAt(%d,%d) mismatch", tc.off, tc.n)
		}
	}
}

// TestIsZstdFile checks content-based detection against the zstd magic, and
// that gzip/raw files are not misclassified.
func TestIsZstdFile(t *testing.T) {
	dir := t.TempDir()
	zst := filepath.Join(dir, "image.img") // .img name on purpose (cached form)
	if err := os.WriteFile(zst, encodeSeekable(t, []byte("hello wendyos image"), 8), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isZstdFile(zst) {
		t.Errorf("isZstdFile(seekable-zst) = false, want true")
	}
	gz := filepath.Join(dir, "g.bin")
	if err := os.WriteFile(gz, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o644); err != nil {
		t.Fatal(err)
	}
	if isZstdFile(gz) {
		t.Errorf("isZstdFile(gzip) = true, want false")
	}
	raw := filepath.Join(dir, "raw.bin")
	if err := os.WriteFile(raw, bytes.Repeat([]byte{0}, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	if isZstdFile(raw) {
		t.Errorf("isZstdFile(raw) = true, want false")
	}
}

// TestStreamZstdImage verifies the full-image stream path decompresses a
// seekable-zstd file back to the exact source and reports the exact size.
func TestStreamZstdImage(t *testing.T) {
	data := make([]byte, 20_000)
	for i := range data {
		data[i] = byte((i * 7) % 253)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "image.img.zst")
	if err := os.WriteFile(path, encodeSeekable(t, data, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	stream, err := streamZstdImage(path)
	if err != nil {
		t.Fatalf("streamZstdImage: %v", err)
	}
	defer stream.Close()

	if stream.uncompressedSize != int64(len(data)) {
		t.Errorf("uncompressedSize = %d, want %d", stream.uncompressedSize, len(data))
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("streamed bytes != source (%d vs %d)", len(got), len(data))
	}
}
