package commands

import (
	"context"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// maxConcurrentLayerPush bounds how many layers are decompressed, chunked, and
// streamed at once. It overlaps the CPU-bound work of one layer with another's
// QueryChunks/WriteChunks network round-trips. Each in-flight layer spills its
// uncompressed tar to a temp file (not RAM), so the cap mainly bounds transient
// chunking buffers rather than whole layers. Chunking within a single layer is
// already parallelized across cores (chunk.ChunkReaderAt), so this need not
// equal the core count.
const maxConcurrentLayerPush = 4

// pushLayersByChunks implements chunk-diff layer push for a set of OCI layers,
// processing up to maxConcurrentLayerPush layers concurrently. For each layer it:
//  1. Resolves the chunk manifest (DiffID, size, ordered chunk hashes) from the
//     on-disk manifest cache when the layer's compressed digest is known,
//     otherwise decompresses (to a temp file) and CDC-chunks the raw tar and
//     caches the result.
//  2. Queries the device for which chunk hashes are missing.
//  3. Streams only the missing chunk bytes via WriteChunks — decompressing the
//     layer at this point if the cache hit let us skip it earlier.
//  4. Produces one RunContainerLayerHeader per layer (COMPRESSION_NONE, carrying
//     the full ordered chunk manifest), in the original layer order.
//
// The common case — an unchanged layer whose chunks the device already has —
// resolves from cache and finds nothing missing, so the layer is never
// decompressed or re-chunked.
func pushLayersByChunks(ctx context.Context, cs agentpb.WendyContainerServiceClient, layers []localLayer) ([]*agentpb.RunContainerLayerHeader, error) {
	headers := make([]*agentpb.RunContainerLayerHeader, len(layers))

	limit := maxConcurrentLayerPush
	if n := runtime.GOMAXPROCS(0); n < limit {
		limit = n
	}
	if limit > len(layers) {
		limit = len(layers)
	}
	if limit < 1 {
		limit = 1
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(limit)
	for i, l := range layers {
		i, l := i, l
		g.Go(func() error {
			h, err := pushLayerByChunks(ctx, cs, l)
			if err != nil {
				return err
			}
			headers[i] = h // distinct index per goroutine — preserves layer order
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return headers, nil
}

// pushLayerByChunks runs the chunk-diff push for a single layer and returns its
// reassembly header. The uncompressed tar is spilled to a temp file rather than
// held in RAM; missing chunk bytes are read back from it on demand.
func pushLayerByChunks(ctx context.Context, cs agentpb.WendyContainerServiceClient, l localLayer) (*agentpb.RunContainerLayerHeader, error) {
	var (
		diffID        string
		size          int64
		orderedHashes [][]byte           // ordered raw 32-byte hashes, for the manifest + QueryChunks
		dl            *decompressedLayer // file-backed tar; populated only when we must produce chunk bytes
		refs          []chunk.Ref        // chunk offsets into dl; populated alongside dl
	)
	defer func() {
		if dl != nil {
			dl.Close()
		}
	}()

	// decompressAndChunk spills the layer to a temp file and chunks it, filling
	// dl/refs/diffID/size. Both entry points (CLI here and the agent) run the
	// identical region+FastCDC algorithm, so these hashes match the device's.
	decompressAndChunk := func() error {
		d, err := decompressLayerToTemp(l)
		if err != nil {
			return err
		}
		dl = d
		r, err := chunk.ChunkReaderAt(d.f, d.size)
		if err != nil {
			return err
		}
		refs, diffID, size = r, d.diffID, d.size
		return nil
	}

	if cm, ok := loadManifestCache(l.Digest); ok {
		diffID, size, orderedHashes = cm.DiffID, cm.Size, cm.Hashes
	} else {
		if err := decompressAndChunk(); err != nil {
			return nil, err
		}
		orderedHashes = make([][]byte, len(refs))
		for i, rf := range refs {
			h := rf.Hash // copy to avoid aliasing the loop variable
			orderedHashes[i] = h[:]
		}
		saveManifestCache(l.Digest, &cachedManifest{DiffID: diffID, Size: size, Hashes: orderedHashes})
	}

	qresp, err := cs.QueryChunks(ctx, &agentpb.QueryChunksRequest{ChunkHashes: orderedHashes})
	if err != nil {
		return nil, err
	}
	missing := make(map[[32]byte]bool, len(qresp.GetMissingHashes()))
	for _, hb := range qresp.GetMissingHashes() {
		var h [32]byte
		copy(h[:], hb)
		missing[h] = true
	}

	if len(missing) > 0 {
		// The device needs some chunks, so we must produce their bytes. If a
		// cache hit let us skip decompression above, do it now. Re-chunking here
		// reproduces the exact hashes in `missing` only because chunking is
		// deterministic and loadManifestCache rejects manifests from a different
		// AlgoVersion — so the cached hashes always match what ChunkReaderAt emits.
		if dl == nil {
			if err := decompressAndChunk(); err != nil {
				return nil, err
			}
		}
		wc, err := cs.WriteChunks(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			if !missing[r.Hash] {
				continue
			}
			buf := make([]byte, r.Len) // r.Len <= chunk.MaxSize (256 KiB)
			if _, err := dl.f.ReadAt(buf, int64(r.Offset)); err != nil {
				return nil, err
			}
			hb := r.Hash // copy
			if err := wc.Send(&agentpb.WriteChunksRequest{
				Hash: hb[:],
				Data: buf,
			}); err != nil {
				return nil, err
			}
		}
		if _, err := wc.CloseAndRecv(); err != nil {
			return nil, err
		}
	}

	return &agentpb.RunContainerLayerHeader{
		Digest:      diffID,
		DiffId:      diffID,
		Size:        size,
		Compression: agentpb.RunContainerLayerHeader_COMPRESSION_NONE,
		ChunkHashes: orderedHashes,
	}, nil
}
