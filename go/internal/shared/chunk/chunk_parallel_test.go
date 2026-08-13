package chunk

import (
	"bytes"
	"crypto/rand"
	"io"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
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

// Every entry point used by the CLI and agent MUST agree byte-for-byte, or
// device-side dedup breaks.
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
		var progress int64
		fromStream, streamed, err := ChunkStream(bytes.NewReader(data), func(completed int64) {
			if completed < progress {
				t.Errorf("size %d: ChunkStream progress moved backward: %d after %d", size, completed, progress)
			}
			progress = completed
		})
		if err != nil {
			t.Fatalf("size %d: ChunkStream: %v", size, err)
		}
		if streamed != int64(size) || progress != int64(size) {
			t.Fatalf("size %d: ChunkStream bytes/progress = %d/%d", size, streamed, progress)
		}
		if !refsEqual(fromBytes, fromStream) {
			t.Fatalf("size %d: ChunkBytes vs ChunkStream differ (%d vs %d chunks)", size, len(fromBytes), len(fromStream))
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

type blockingReaderAt struct {
	current atomic.Int32
	peak    atomic.Int32
	release <-chan struct{}
}

func (r *blockingReaderAt) ReadAt(p []byte, _ int64) (int, error) {
	current := r.current.Add(1)
	for peak := r.peak.Load(); current > peak && !r.peak.CompareAndSwap(peak, current); peak = r.peak.Load() {
	}
	<-r.release
	clear(p)
	r.current.Add(-1)
	return len(p), nil
}

func TestChunkReaderAtBoundsRegionBuffersProcessWide(t *testing.T) {
	originalProcs := runtime.GOMAXPROCS(maxConcurrentReaderAtRegions + 2)
	t.Cleanup(func() { runtime.GOMAXPROCS(originalProcs) })

	release := make(chan struct{})
	reader := &blockingReaderAt{release: release}
	done := make(chan error, 3)
	// Several independent calls model concurrent Compose services/layers. A
	// per-call limit would still allow all six regions through; only the shared
	// admission pool keeps their aggregate at four.
	for range 3 {
		go func() {
			_, err := ChunkReaderAt(reader, int64(2*regionSize))
			done <- err
		}()
	}

	deadline := time.After(2 * time.Second)
	for reader.current.Load() < maxConcurrentReaderAtRegions {
		select {
		case <-deadline:
			close(release)
			t.Fatal("ChunkReaderAt did not fill the expected region-worker slots")
		default:
			runtime.Gosched()
		}
	}
	// Give every extra region goroutine a chance to run. Without the global
	// slots, peak would rise above four while all reads are held at the barrier.
	time.Sleep(20 * time.Millisecond)
	if peak := reader.peak.Load(); peak != maxConcurrentReaderAtRegions {
		close(release)
		t.Fatalf("simultaneous ReaderAt regions = %d, want %d", peak, maxConcurrentReaderAtRegions)
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
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
