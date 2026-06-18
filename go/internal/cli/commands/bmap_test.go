package commands

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestParseBmap(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.bmap")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	b, err := parseBmap(data)
	if err != nil {
		t.Fatalf("parseBmap: %v", err)
	}
	if b.BlockSize != 4096 {
		t.Errorf("BlockSize = %d, want 4096", b.BlockSize)
	}
	if b.ImageSize != 32768 {
		t.Errorf("ImageSize = %d, want 32768", b.ImageSize)
	}
	if len(b.Ranges) != 2 {
		t.Fatalf("len(Ranges) = %d, want 2", len(b.Ranges))
	}
	if b.Ranges[0].First != 0 || b.Ranges[0].Last != 1 {
		t.Errorf("Ranges[0] = %d-%d, want 0-1", b.Ranges[0].First, b.Ranges[0].Last)
	}
	if b.Ranges[1].First != 4 || b.Ranges[1].Last != 5 {
		t.Errorf("Ranges[1] = %d-%d, want 4-5", b.Ranges[1].First, b.Ranges[1].Last)
	}
}

func TestParseBmapSingleBlockRange(t *testing.T) {
	const x = `<bmap version="2.0"><ImageSize>4096</ImageSize><BlockSize>4096</BlockSize>` +
		`<ChecksumType>sha256</ChecksumType><BlockMap><Range chksum="ab">3</Range></BlockMap></bmap>`
	b, err := parseBmap([]byte(x))
	if err != nil {
		t.Fatalf("parseBmap: %v", err)
	}
	if len(b.Ranges) != 1 || b.Ranges[0].First != 3 || b.Ranges[0].Last != 3 {
		t.Fatalf("range = %+v, want single block 3", b.Ranges)
	}
}

func TestParseBmapRejectsNonSHA256(t *testing.T) {
	const x = `<bmap version="2.0"><ImageSize>1</ImageSize><BlockSize>1</BlockSize>` +
		`<ChecksumType>crc32</ChecksumType><BlockMap></BlockMap></bmap>`
	if _, err := parseBmap([]byte(x)); err == nil {
		t.Fatal("expected error for non-sha256 checksum type")
	}
}

func TestParseBmapRejectsGarbage(t *testing.T) {
	if _, err := parseBmap([]byte("not xml")); err == nil {
		t.Fatal("expected error for invalid XML")
	}
}

// memWriterAt is an in-memory io.WriterAt that records exactly which offsets
// were written, so tests can assert holes are left untouched.
type memWriterAt struct {
	buf     []byte
	written []bool // per-byte: true if written
}

func newMemWriterAt(size int64) *memWriterAt {
	return &memWriterAt{buf: make([]byte, size), written: make([]bool, size)}
}

func (m *memWriterAt) WriteAt(p []byte, off int64) (int, error) {
	copy(m.buf[off:], p)
	for i := range p {
		m.written[off+int64(i)] = true
	}
	return len(p), nil
}

func blockChecksum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildSparseImage returns an 8-block image (block size 4096) where blocks 0-1
// are zeros and blocks 4-5 are 0x01; blocks 2-3 and 6-7 are "holes" (zeros) we
// expect applyBmap NOT to write.
func buildSparseImage() ([]byte, *Bmap) {
	const bs = 4096
	img := make([]byte, bs*8)
	for i := bs * 4; i < bs*6; i++ {
		img[i] = 0x01
	}
	b := &Bmap{
		BlockSize: bs,
		ImageSize: int64(len(img)),
		Ranges: []BmapRange{
			{First: 0, Last: 1, Checksum: blockChecksum(img[0 : bs*2])},
			{First: 4, Last: 5, Checksum: blockChecksum(img[bs*4 : bs*6])},
		},
	}
	return img, b
}

func TestApplyBmapWritesOnlyMappedRanges(t *testing.T) {
	img, b := buildSparseImage()
	dst := newMemWriterAt(b.ImageSize)
	var progress int64
	if err := applyBmap(bytes.NewReader(img), dst, b, func(n int64) { progress = n }); err != nil {
		t.Fatalf("applyBmap: %v", err)
	}
	const bs = 4096
	if !bytes.Equal(dst.buf[0:bs*2], img[0:bs*2]) || !bytes.Equal(dst.buf[bs*4:bs*6], img[bs*4:bs*6]) {
		t.Fatal("mapped ranges not written correctly")
	}
	for _, off := range []int{bs * 2, bs*4 - 1, bs * 6, bs*8 - 1} {
		if dst.written[off] {
			t.Fatalf("hole byte at offset %d was written", off)
		}
	}
	if progress != b.ImageSize {
		t.Fatalf("progress = %d, want %d", progress, b.ImageSize)
	}
}

func TestApplyBmapDetectsCorruption(t *testing.T) {
	img, b := buildSparseImage()
	img[10] = 0xff // corrupt a byte inside mapped block 0
	dst := newMemWriterAt(b.ImageSize)
	err := applyBmap(bytes.NewReader(img), dst, b, func(int64) {})
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
}

func TestApplyBmapMultiChunkRange(t *testing.T) {
	old := bmapChunkSize
	bmapChunkSize = 7 // tiny, to force many inner-loop iterations per range
	defer func() { bmapChunkSize = old }()

	img, b := buildSparseImage() // blocks 0-1 and 4-5 mapped, bs=4096
	dst := newMemWriterAt(b.ImageSize)
	if err := applyBmap(bytes.NewReader(img), dst, b, func(int64) {}); err != nil {
		t.Fatalf("applyBmap: %v", err)
	}
	const bs = 4096
	if !bytes.Equal(dst.buf[0:bs*2], img[0:bs*2]) || !bytes.Equal(dst.buf[bs*4:bs*6], img[bs*4:bs*6]) {
		t.Fatal("mapped ranges not reconstructed correctly across chunk boundaries")
	}
}

func TestApplyBmapPartialFinalBlock(t *testing.T) {
	const bs = 4096
	const size = bs + 100 // block 0 full, block 1 only 100 bytes
	img := make([]byte, size)
	for i := range img {
		img[i] = byte(i % 251)
	}
	b := &Bmap{
		BlockSize: bs,
		ImageSize: size,
		Ranges:    []BmapRange{{First: 0, Last: 1, Checksum: blockChecksum(img[:size])}},
	}
	dst := newMemWriterAt(size)
	if err := applyBmap(bytes.NewReader(img), dst, b, func(int64) {}); err != nil {
		t.Fatalf("applyBmap: %v", err)
	}
	if !bytes.Equal(dst.buf, img) {
		t.Fatal("partial final block not written correctly")
	}
	if dst.written[size-1] != true {
		t.Fatal("last byte should be written")
	}
}

func TestApplyBmapEmptyChecksumSkipsVerify(t *testing.T) {
	img, b := buildSparseImage()
	img[10] = 0xff            // corrupt mapped block 0
	b.Ranges[0].Checksum = "" // but disable its verification
	b.Ranges[1].Checksum = "" // and the other, so neither aborts
	dst := newMemWriterAt(b.ImageSize)
	if err := applyBmap(bytes.NewReader(img), dst, b, func(int64) {}); err != nil {
		t.Fatalf("applyBmap with empty checksums should not verify: %v", err)
	}
}

func TestParseBmapRejectsZeroSizes(t *testing.T) {
	for _, x := range []string{
		`<bmap version="2.0"><ImageSize>0</ImageSize><BlockSize>4096</BlockSize><ChecksumType>sha256</ChecksumType><BlockMap></BlockMap></bmap>`,
		`<bmap version="2.0"><ImageSize>4096</ImageSize><BlockSize>0</BlockSize><ChecksumType>sha256</ChecksumType><BlockMap></BlockMap></bmap>`,
	} {
		if _, err := parseBmap([]byte(x)); err == nil {
			t.Fatalf("expected error for zero size in %q", x)
		}
	}
}

func TestApplyBmapSeekableWritesOnlyMappedRanges(t *testing.T) {
	img, b := buildSparseImage() // blocks 0-1 and 4-5 mapped, bs=4096
	comp := encodeSeekable(t, img, 4096)
	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("open seekable: %v", err)
	}
	defer si.Close()

	dst := newMemWriterAt(b.ImageSize)
	var progress int64
	mapped := mappedBytes(b)
	if err := applyBmapSeekable(si, dst, b, func(n int64) { progress = n }); err != nil {
		t.Fatalf("applyBmapSeekable: %v", err)
	}
	const bs = 4096
	if !bytes.Equal(dst.buf[0:bs*2], img[0:bs*2]) || !bytes.Equal(dst.buf[bs*4:bs*6], img[bs*4:bs*6]) {
		t.Fatal("mapped ranges not written correctly")
	}
	for _, off := range []int{bs * 2, bs*4 - 1, bs * 6, bs*8 - 1} {
		if dst.written[off] {
			t.Fatalf("hole byte at offset %d was written", off)
		}
	}
	if progress != mapped {
		t.Fatalf("progress = %d, want mapped total %d", progress, mapped)
	}
}

// TestApplyBmapSeekableConcurrentWriters stresses the pipelined writer path:
// tiny chunks force many buffers to cycle through the pool while multiple
// writer goroutines drain them out of order. A buffer-aliasing bug (the reader
// overwriting a buffer still queued for write) would corrupt the output, and a
// non-monotonic/racy progress counter would surface here under -race.
func TestApplyBmapSeekableConcurrentWriters(t *testing.T) {
	oldChunk, oldWorkers := bmapChunkSize, bmapWriteConcurrency
	bmapChunkSize = 13       // tiny: many chunks per range, forces pool cycling
	bmapWriteConcurrency = 4 // exercise multiple writer goroutines
	defer func() { bmapChunkSize, bmapWriteConcurrency = oldChunk, oldWorkers }()

	const bs = 4096
	const blocks = 64
	img := make([]byte, bs*blocks)
	for i := range img {
		img[i] = byte((i*7 + 3) % 256)
	}
	// Three mapped ranges separated by holes.
	b := &Bmap{
		BlockSize: bs,
		ImageSize: int64(len(img)),
		Ranges: []BmapRange{
			{First: 0, Last: 3, Checksum: blockChecksum(img[0 : bs*4])},
			{First: 10, Last: 20, Checksum: blockChecksum(img[bs*10 : bs*21])},
			{First: 60, Last: 63, Checksum: blockChecksum(img[bs*60 : bs*64])},
		},
	}
	comp := encodeSeekable(t, img, bs)
	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("open seekable: %v", err)
	}
	defer si.Close()

	dst := newMemWriterAt(b.ImageSize)
	var progress int64 // safe: applyBmapSeekable serializes progressFn calls
	if err := applyBmapSeekable(si, dst, b, func(n int64) { progress = n }); err != nil {
		t.Fatalf("applyBmapSeekable: %v", err)
	}
	for _, r := range b.Ranges {
		s, e := r.First*bs, (r.Last+1)*bs
		if !bytes.Equal(dst.buf[s:e], img[s:e]) {
			t.Fatalf("range %d-%d not reconstructed correctly", r.First, r.Last)
		}
	}
	for _, off := range []int{bs * 4, bs*10 - 1, bs * 21, bs*60 - 1} {
		if dst.written[off] {
			t.Fatalf("hole byte at offset %d was written", off)
		}
	}
	if progress != mappedBytes(b) {
		t.Fatalf("progress = %d, want %d", progress, mappedBytes(b))
	}
}

func TestWritersForStorage(t *testing.T) {
	if got := writersForStorage(StorageNVMe); got != 4 {
		t.Errorf("NVMe writers = %d, want 4", got)
	}
	// SD/USB and unknown devices must stay strictly sequential.
	for _, st := range []StorageType{StorageUSB, StorageUnknown} {
		if got := writersForStorage(st); got != 1 {
			t.Errorf("writers for %v = %d, want 1 (sequential)", st, got)
		}
	}
}

func TestApplyBmapSeekableDetectsCorruption(t *testing.T) {
	img, b := buildSparseImage()
	img[10] = 0xff // corrupt mapped block 0 before compression
	comp := encodeSeekable(t, img, 4096)
	si, err := openSeekableZstdFromReader(bytes.NewReader(comp))
	if err != nil {
		t.Fatalf("open seekable: %v", err)
	}
	defer si.Close()
	dst := newMemWriterAt(b.ImageSize)
	if err := applyBmapSeekable(si, dst, b, func(int64) {}); err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
}
