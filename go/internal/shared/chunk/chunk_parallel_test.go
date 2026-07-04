package chunk

import (
	"bytes"
	"crypto/rand"
	"io"
	"runtime"
	"testing"
)

// randData returns n bytes that are deterministic within a single test run but
// content-varied enough to exercise content-defined cut points.
func randData(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func refsEqual(a, b []Ref) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The three entry points used by the two callers (CLI: ChunkBytes over an
// in-memory tar; agent: ChunkReaderAt over the content store; and the legacy
// Chunk(io.Reader)) MUST agree byte-for-byte, or device-side dedup breaks.
func TestEntryPointParity(t *testing.T) {
	for _, size := range []int{0, 1, 1 << 10, int(MinSize), int(MaxSize) + 1, regionSize - 1, regionSize, regionSize + 7, 3*regionSize + 12345} {
		data := randData(t, size)

		fromBytes, err := ChunkBytes(data)
		if err != nil {
			t.Fatalf("size %d: ChunkBytes: %v", size, err)
		}
		fromReaderAt, err := ChunkReaderAt(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("size %d: ChunkReaderAt: %v", size, err)
		}
		fromReader, err := Chunk(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size %d: Chunk: %v", size, err)
		}
		if !refsEqual(fromBytes, fromReaderAt) {
			t.Fatalf("size %d: ChunkBytes vs ChunkReaderAt differ (%d vs %d chunks)", size, len(fromBytes), len(fromReaderAt))
		}
		if !refsEqual(fromBytes, fromReader) {
			t.Fatalf("size %d: ChunkBytes vs Chunk differ (%d vs %d chunks)", size, len(fromBytes), len(fromReader))
		}
	}
}

// Output must not depend on how many workers the parallel driver uses.
func TestDeterministicAcrossParallelism(t *testing.T) {
	data := randData(t, 3*regionSize+9999)
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)

	runtime.GOMAXPROCS(1)
	serial, err := ChunkBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GOMAXPROCS(8)
	parallel, err := ChunkBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if !refsEqual(serial, parallel) {
		t.Fatalf("chunking changed with worker count: %d vs %d chunks", len(serial), len(parallel))
	}
}

// Chunks must cover the stream contiguously and never exceed MaxSize, even
// across region seams (a multi-region input).
func TestMultiRegionCoverageAndBounds(t *testing.T) {
	data := randData(t, 3*regionSize+4242)
	refs, err := ChunkBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	var off uint64
	for i, r := range refs {
		if r.Offset != off {
			t.Fatalf("chunk %d offset gap: got %d want %d", i, r.Offset, off)
		}
		if r.Len == 0 {
			t.Fatalf("chunk %d has zero length", i)
		}
		if r.Len > MaxSize {
			t.Fatalf("chunk %d len %d exceeds MaxSize %d", i, r.Len, MaxSize)
		}
		// A chunk may fall below MinSize only when it is the tail of a region
		// (forced seam at a regionSize boundary) or the tail of the whole stream.
		end := r.Offset + r.Len
		if r.Len < MinSize && end%regionSize != 0 && end != uint64(len(data)) {
			t.Fatalf("chunk %d len %d below MinSize %d but not a region/stream tail (ends at %d)", i, r.Len, MinSize, end)
		}
		off += r.Len
	}
	if off != uint64(len(data)) {
		t.Fatalf("chunks cover %d bytes, want %d", off, len(data))
	}
}

// A short read from the ReaderAt must surface as an error, not silent truncation.
type shortReaderAt struct{ n int }

func (s shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestChunkReaderAtPropagatesError(t *testing.T) {
	if _, err := ChunkReaderAt(shortReaderAt{}, int64(regionSize+1)); err == nil {
		t.Fatal("expected error from failing ReaderAt, got nil")
	}
}

func BenchmarkChunkBytes(b *testing.B) {
	data := make([]byte, 256<<20) // 256 MiB
	if _, err := rand.Read(data); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ChunkBytes(data); err != nil {
			b.Fatal(err)
		}
	}
}
