// Package chunk provides content-defined chunking shared by the CLI and agent
// for sub-layer diffing. Boundaries come from a FastCDC gear-hash chunker; each
// chunk is identified by the sha256 of its bytes.
//
// To use every CPU core on large layers the input is split into fixed-size
// regions (regionSize) that are chunked concurrently, with a forced boundary at
// each region seam. The region size and the gear table are part of the on-the-
// wire contract: the CLI and the agent both run this exact algorithm so the
// chunk hashes they compute for the same bytes match. Changing AlgoVersion,
// regionSize, the gear table, or the size constants changes every hash and must
// be treated as a format break (see AlgoVersion).
package chunk

import (
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"runtime"

	"golang.org/x/sync/errgroup"
)

const (
	MinSize uint64 = 16 << 10
	AvgSize uint64 = 64 << 10
	MaxSize uint64 = 256 << 10
)

// AlgoVersion identifies the chunking algorithm. Bump it whenever a change here
// would alter the chunk boundaries or hashes for the same input (gear table,
// masks, size constants, or region size). Callers that persist chunk manifests
// use it to reject stale caches produced by an older algorithm.
const AlgoVersion = 2

// regionSize is the granularity of parallel chunking. The input is cut into
// regions of this size and each is chunked independently, so a forced boundary
// falls on every multiple of regionSize. It is much larger than MaxSize, so the
// dedup cost of these forced seams is ~one chunk per region (<0.5%), while still
// yielding hundreds of regions for a multi-GiB layer — plenty of parallelism.
const regionSize = 16 << 20 // 16 MiB

// FastCDC normalized-chunking masks. AvgSize is 2^16, so the target is 16 hash
// bits; normalization makes cuts harder than target before AvgSize (maskS, 18
// bits) and easier after it (maskL, 14 bits), tightening the size distribution
// around AvgSize. These are low-bit masks, which suits the gear hash that
// accumulates recent bytes in its low bits.
const (
	maskS uint64 = (1 << 18) - 1
	maskL uint64 = (1 << 14) - 1
)

// gear is the FastCDC gear table: one pseudo-random value per byte value. It is
// generated from a fixed seed so it is identical on every build and platform —
// the value of these numbers is part of the chunking contract (see AlgoVersion).
var gear [256]uint64

func init() {
	r := rand.New(rand.NewSource(0x6368756e6b5f7632)) // "chunk_v2"
	for i := range gear {
		gear[i] = r.Uint64()
	}
}

// Ref identifies one content-defined chunk and its position in the stream.
type Ref struct {
	Hash   [32]byte
	Offset uint64
	Len    uint64
}

// Chunk reads r to completion and returns its content-defined chunks in order.
// It buffers the whole input in memory; callers that already hold the bytes or
// have random access should prefer ChunkBytes / ChunkReaderAt.
func Chunk(r io.Reader) ([]Ref, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ChunkBytes(data)
}

// ChunkBytes returns the content-defined chunks of an in-memory buffer. Regions
// are chunked concurrently and reference data directly (no copy).
func ChunkBytes(data []byte) ([]Ref, error) {
	n := len(data)
	if n == 0 {
		return nil, nil
	}
	if n <= regionSize {
		return fastCDCRegion(data, 0), nil
	}
	return chunkRegions(n, func(start, end int) ([]Ref, error) {
		return fastCDCRegion(data[start:end], uint64(start)), nil
	})
}

// ChunkReaderAt returns the content-defined chunks of a random-access source of
// the given size, identical to ChunkBytes over the same bytes. Each region is
// read into a transient buffer, so peak memory is bounded by regionSize times
// the worker count rather than the whole input — important on the agent, which
// re-chunks multi-GiB layers without holding them in RAM.
func ChunkReaderAt(ra io.ReaderAt, size int64) ([]Ref, error) {
	if size < 0 {
		return nil, fmt.Errorf("chunk: negative size %d", size)
	}
	n := int(size)
	if int64(n) != size {
		return nil, fmt.Errorf("chunk: size %d too large for this platform", size)
	}
	if n == 0 {
		return nil, nil
	}
	return chunkRegions(n, func(start, end int) ([]Ref, error) {
		buf := make([]byte, end-start)
		// io.ReaderAt fills buf fully unless it returns an error; a full read may
		// still report io.EOF, so trust the byte count rather than the error.
		if got, err := ra.ReadAt(buf, int64(start)); got != len(buf) {
			return nil, fmt.Errorf("chunk: short read at %d: %d/%d bytes: %w", start, got, len(buf), err)
		}
		return fastCDCRegion(buf, uint64(start)), nil
	})
}

// chunkRegions splits [0,n) into regionSize-aligned regions, runs work on each
// concurrently, and concatenates the results in region order. The output is
// independent of the worker count, so the result is fully deterministic.
func chunkRegions(n int, work func(start, end int) ([]Ref, error)) ([]Ref, error) {
	numRegions := (n + regionSize - 1) / regionSize
	parts := make([][]Ref, numRegions)

	workers := runtime.GOMAXPROCS(0)
	if workers > numRegions {
		workers = numRegions
	}
	var g errgroup.Group
	g.SetLimit(workers)
	for i := 0; i < numRegions; i++ {
		start := i * regionSize
		end := start + regionSize
		if end > n {
			end = n
		}
		i, start, end := i, start, end
		g.Go(func() error {
			refs, err := work(start, end)
			if err != nil {
				return err
			}
			parts[i] = refs
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	total := 0
	for _, p := range parts {
		total += len(p)
	}
	out := make([]Ref, 0, total)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out, nil
}

// fastCDCRegion chunks one contiguous region with FastCDC and returns the chunk
// refs, whose Offset is relative to the whole stream (base + position in data).
// The region is covered contiguously; the final chunk runs to the region end
// (the forced seam between regions).
func fastCDCRegion(data []byte, base uint64) []Ref {
	var refs []Ref
	for pos := 0; pos < len(data); {
		cut := cutpoint(data[pos:])
		buf := data[pos : pos+cut]
		refs = append(refs, Ref{
			Hash:   sha256.Sum256(buf),
			Offset: base + uint64(pos),
			Len:    uint64(cut),
		})
		pos += cut
	}
	return refs
}

// cutpoint returns the length of the next chunk at the start of data, using
// FastCDC with normalized chunking (Xia et al., USENIX ATC 2016, Algorithm 2).
// The result is in [MinSize, MaxSize] unless data is shorter than MinSize, in
// which case the whole remainder is one chunk.
func cutpoint(data []byte) int {
	n := len(data)
	if n <= int(MinSize) {
		return n
	}
	if n > int(MaxSize) {
		n = int(MaxSize)
	}
	normal := int(AvgSize)
	if normal > n {
		normal = n
	}

	var hash uint64
	i := int(MinSize)
	for ; i < normal; i++ {
		hash = (hash << 1) + gear[data[i]]
		if hash&maskS == 0 {
			return i
		}
	}
	for ; i < n; i++ {
		hash = (hash << 1) + gear[data[i]]
		if hash&maskL == 0 {
			return i
		}
	}
	return n
}
