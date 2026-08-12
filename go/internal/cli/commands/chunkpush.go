package commands

import (
	"context"
	"fmt"
	"runtime"

	"golang.org/x/sync/errgroup"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
// decompressed or re-chunked. A layer the device already holds in full (reported
// by the QueryLayers pre-check) is skipped entirely: it is never decompressed,
// chunked, or transferred.
func pushLayersByChunks(ctx context.Context, cs agentpb.WendyContainerServiceClient, layers []localLayer) ([]*agentpb.RunContainerLayerHeader, error) {
	return pushLayersByChunksWithPrepare(ctx, cs, layers, nil)
}

// imagePrepareFunc starts an authenticated device-side preparation using the
// complete ordered layer manifests. It is invoked after manifest resolution
// but before any missing chunk upload, and is expected to block until the
// preparation is complete (or the context is cancelled).
type imagePrepareFunc func(context.Context, []*agentpb.RunContainerLayerHeader) error

// pushLayersByChunksWithPrepare resolves every layer manifest first, then runs
// prepare concurrently with missing-chunk upload. The short resolution barrier
// is necessary because the agent must authenticate the image config (which
// binds the ordered diff IDs) and know each layer's chunk set before it can
// safely create persistent snapshots.
func pushLayersByChunksWithPrepare(ctx context.Context, cs agentpb.WendyContainerServiceClient, layers []localLayer, prepare imagePrepareFunc) ([]*agentpb.RunContainerLayerHeader, error) {
	headers := make([]*agentpb.RunContainerLayerHeader, len(layers))

	// Capability probe: a single empty QueryChunks tells us whether the agent
	// supports chunk-diff at all BEFORE we decompress and chunk the first layer.
	// An old agent returns Unimplemented, which bubbles up so deployByChunkDiff
	// falls back to a registry push instead of wasting a layer's worth of work.
	if _, err := cs.QueryChunks(ctx, &agentpb.QueryChunksRequest{}); err != nil {
		return nil, err
	}

	// Layer pre-check: ask which layers the device already has by diff ID so we
	// can skip them entirely. A layer the device already holds yields no dedup, so
	// decompressing and content-chunking it would be pure waste. Degrades to
	// chunking every layer when the agent is too old or the query fails.
	present := queryPresentLayers(ctx, cs, layers)

	// Build the present-layer headers up front and collect the indices that still
	// need a chunk-diff push. A present-layer header carries no chunk hashes, so
	// the agent skips reassembly and reuses the blob already in its content store.
	// We trust that blob for the rest of the deploy: unlike the always-send-chunks
	// path (where QueryChunks would re-report bytes the device lost mid-deploy),
	// nothing re-sends a skipped layer. The window is safe in practice — the blob
	// stays referenced by the app's current image until this deploy replaces it,
	// so containerd GC will not reclaim it underneath us.
	var toPush []int
	skipped := 0
	for i, l := range layers {
		if l.DiffID != "" {
			if size, ok := present[l.DiffID]; ok {
				headers[i] = &agentpb.RunContainerLayerHeader{
					Digest:      l.DiffID,
					DiffId:      l.DiffID,
					Size:        size,
					Compression: agentpb.RunContainerLayerHeader_COMPRESSION_NONE,
				}
				skipped++
				continue
			}
		}
		toPush = append(toPush, i)
	}
	if skipped > 0 {
		cliLogln("Reusing %s layer(s) already on device; chunking %s.",
			tui.Value(fmt.Sprintf("%d", skipped)), tui.Value(fmt.Sprintf("%d", len(toPush))))
	}
	limit := maxConcurrentLayerPush
	if n := runtime.GOMAXPROCS(0); n < limit {
		limit = n
	}
	if limit > len(toPush) {
		limit = len(toPush)
	}
	if limit < 1 {
		limit = 1
	}

	resolved := make([]*resolvedChunkLayer, len(layers))
	defer func() {
		for _, r := range resolved {
			if r != nil {
				r.close()
			}
		}
	}()
	var resolveGroup errgroup.Group
	resolveGroup.SetLimit(limit)
	for _, idx := range toPush {
		idx, l := idx, layers[idx]
		resolveGroup.Go(func() error {
			r, err := resolveChunkLayer(l)
			if err != nil {
				return err
			}
			resolved[idx] = r
			headers[idx] = r.header // distinct index per goroutine — preserves layer order
			return nil
		})
	}
	if err := resolveGroup.Wait(); err != nil {
		return nil, err
	}

	prepareCtx, cancelPrepare := context.WithCancel(ctx)
	defer cancelPrepare()
	var prepareDone chan error
	if prepare != nil {
		prepareDone = make(chan error, 1)
		go func() {
			prepareDone <- prepare(prepareCtx, headers)
		}()
	}

	uploadGroup, uploadCtx := errgroup.WithContext(ctx)
	uploadGroup.SetLimit(limit)
	for _, idx := range toPush {
		r := resolved[idx]
		uploadGroup.Go(func() error {
			return r.upload(uploadCtx, cs)
		})
	}
	if err := uploadGroup.Wait(); err != nil {
		cancelPrepare()
		if prepareDone != nil {
			<-prepareDone
		}
		return nil, err
	}

	if prepareDone != nil {
		if err := <-prepareDone; err != nil {
			switch status.Code(err) {
			case codes.InvalidArgument, codes.FailedPrecondition, codes.DataLoss, codes.PermissionDenied, codes.Unauthenticated:
				// Security failures must not silently fall through to the registry
				// path, which has a different trust boundary.
				return nil, err
			case codes.Unimplemented:
				// Older agents use the existing RunContainer assembly/unpack path.
			default:
				// Preparation is an optimization. RunContainer repeats all work
				// idempotently and remains the authoritative error path.
				cliLogln("Image prewarming unavailable (%v); finishing during start.", err)
			}
		}
	}
	return headers, nil
}

// queryPresentLayers asks the device which of these layers it already has, keyed
// by diff ID, returning each present diff ID's uncompressed size. Layers with no
// known diff ID are not queried. The pre-check is a pure optimization: an agent
// too old to implement QueryLayers (Unimplemented), or any query error, yields a
// nil map so the caller chunks every layer as before.
func queryPresentLayers(ctx context.Context, cs agentpb.WendyContainerServiceClient, layers []localLayer) map[string]int64 {
	diffIDs := make([]string, 0, len(layers))
	for _, l := range layers {
		if l.DiffID != "" {
			diffIDs = append(diffIDs, l.DiffID)
		}
	}
	if len(diffIDs) == 0 {
		return nil
	}
	resp, err := cs.QueryLayers(ctx, &agentpb.QueryLayersRequest{DiffIds: diffIDs})
	if err != nil {
		if !isUnimplementedRPCError(err) {
			// The agent supports chunk-diff (the probe succeeded) but the layer
			// pre-check failed for another reason; chunk everything rather than
			// abort the deploy over a missed optimization.
			cliLogln("Layer pre-check unavailable (%v); chunking all layers.", err)
		}
		return nil
	}
	out := make(map[string]int64, len(resp.GetPresent()))
	for _, p := range resp.GetPresent() {
		out[p.GetDiffId()] = p.GetSize()
	}
	return out
}

// pushLayerByChunks runs the chunk-diff push for a single layer and returns its
// reassembly header. The uncompressed tar is spilled to a temp file rather than
// held in RAM; missing chunk bytes are read back from it on demand.
type resolvedChunkLayer struct {
	l      localLayer
	header *agentpb.RunContainerLayerHeader
	dl     *decompressedLayer
	refs   []chunk.Ref
}

func (r *resolvedChunkLayer) close() {
	if r.dl != nil {
		r.dl.Close()
		r.dl = nil
	}
}

// ensureDecompressed materializes and chunks a cache-hit layer only when the
// device reports missing chunk bytes. Cache misses already did this during
// manifest resolution and retain the temporary file through upload.
func (r *resolvedChunkLayer) ensureDecompressed() error {
	if r.dl != nil {
		return nil
	}
	d, err := decompressLayerToTemp(r.l)
	if err != nil {
		return err
	}
	refs, err := chunk.ChunkReaderAt(d.f, d.size)
	if err != nil {
		d.Close()
		return err
	}
	// A cache entry is accepted only for the current chunk algorithm version,
	// so deterministic re-chunking must reproduce its identity and shape.
	if got := d.diffID; got != r.header.GetDiffId() {
		d.Close()
		return fmt.Errorf("cached layer diff ID changed: got %s, want %s", got, r.header.GetDiffId())
	}
	if len(refs) != len(r.header.GetChunkHashes()) {
		d.Close()
		return fmt.Errorf("cached layer chunk count changed: got %d, want %d", len(refs), len(r.header.GetChunkHashes()))
	}
	r.dl, r.refs = d, refs
	return nil
}

func resolveChunkLayer(l localLayer) (*resolvedChunkLayer, error) {
	var (
		diffID        string
		size          int64
		orderedHashes [][]byte           // ordered raw 32-byte hashes, for the manifest + QueryChunks
		dl            *decompressedLayer // file-backed tar; populated only when we must produce chunk bytes
		refs          []chunk.Ref        // chunk offsets into dl; populated alongside dl
	)

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
			d.Close()
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

	return &resolvedChunkLayer{
		l: l,
		header: &agentpb.RunContainerLayerHeader{
			Digest:      diffID,
			DiffId:      diffID,
			Size:        size,
			Compression: agentpb.RunContainerLayerHeader_COMPRESSION_NONE,
			ChunkHashes: orderedHashes,
		},
		dl:   dl,
		refs: refs,
	}, nil
}

func (r *resolvedChunkLayer) upload(ctx context.Context, cs agentpb.WendyContainerServiceClient) error {
	orderedHashes := r.header.GetChunkHashes()
	qresp, err := cs.QueryChunks(ctx, &agentpb.QueryChunksRequest{ChunkHashes: orderedHashes})
	if err != nil {
		return err
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
		if err := r.ensureDecompressed(); err != nil {
			return err
		}
		wc, err := cs.WriteChunks(ctx)
		if err != nil {
			return err
		}
		for _, ref := range r.refs {
			if !missing[ref.Hash] {
				continue
			}
			buf := make([]byte, ref.Len) // ref.Len <= chunk.MaxSize (256 KiB)
			if _, err := r.dl.f.ReadAt(buf, int64(ref.Offset)); err != nil {
				return err
			}
			hb := ref.Hash // copy
			if err := wc.Send(&agentpb.WriteChunksRequest{
				Hash: hb[:],
				Data: buf,
			}); err != nil {
				return err
			}
		}
		if _, err := wc.CloseAndRecv(); err != nil {
			return err
		}
	}
	return nil
}

func pushLayerByChunks(ctx context.Context, cs agentpb.WendyContainerServiceClient, l localLayer) (*agentpb.RunContainerLayerHeader, error) {
	r, err := resolveChunkLayer(l)
	if err != nil {
		return nil, err
	}
	defer r.close()
	if err := r.upload(ctx, cs); err != nil {
		return nil, err
	}
	return r.header, nil
}
