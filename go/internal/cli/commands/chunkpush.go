package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// pushLayersByChunks implements chunk-diff layer push for a set of OCI layers.
// For each layer it:
//  1. Resolves the chunk manifest (DiffID, size, ordered chunk hashes) from the
//     on-disk manifest cache when the layer's compressed digest is known,
//     otherwise decompresses and CDC-chunks the raw tar and caches the result.
//  2. Queries the device for which chunk hashes are missing.
//  3. Streams only the missing chunk bytes via WriteChunks — decompressing the
//     layer at this point if the cache hit let us skip it earlier.
//  4. Returns one RunContainerLayerHeader per layer (COMPRESSION_NONE, carrying
//     the full ordered chunk manifest).
//
// The common case — an unchanged layer whose chunks the device already has —
// resolves from cache and finds nothing missing, so the layer is never
// decompressed or re-chunked.
func pushLayersByChunks(ctx context.Context, cs agentpb.WendyContainerServiceClient, layers []localLayer) ([]*agentpb.RunContainerLayerHeader, error) {
	headers := make([]*agentpb.RunContainerLayerHeader, 0, len(layers))

	for _, l := range layers {
		var (
			diffID        string
			size          int64
			orderedHashes [][]byte    // ordered raw 32-byte hashes, for the manifest + QueryChunks
			tar           []byte      // decompressed bytes; populated only when needed
			refs          []chunk.Ref // chunk offsets; populated only when decompressed
		)

		if cm, ok := loadManifestCache(l.Digest); ok {
			diffID, size, orderedHashes = cm.DiffID, cm.Size, cm.Hashes
		} else {
			t, err := l.decompress()
			if err != nil {
				return nil, err
			}
			r, err := chunk.Chunk(bytes.NewReader(t))
			if err != nil {
				return nil, err
			}
			tar, refs = t, r
			sum := sha256.Sum256(tar)
			diffID = "sha256:" + hex.EncodeToString(sum[:])
			size = int64(len(tar))
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
			// cache hit let us skip decompression above, do it now.
			if tar == nil {
				t, err := l.decompress()
				if err != nil {
					return nil, err
				}
				r, err := chunk.Chunk(bytes.NewReader(t))
				if err != nil {
					return nil, err
				}
				tar, refs = t, r
			}
			wc, err := cs.WriteChunks(ctx)
			if err != nil {
				return nil, err
			}
			for _, r := range refs {
				if !missing[r.Hash] {
					continue
				}
				hb := r.Hash // copy
				if err := wc.Send(&agentpb.WriteChunksRequest{
					Hash: hb[:],
					Data: tar[r.Offset : r.Offset+r.Len],
				}); err != nil {
					return nil, err
				}
			}
			if _, err := wc.CloseAndRecv(); err != nil {
				return nil, err
			}
		}

		headers = append(headers, &agentpb.RunContainerLayerHeader{
			Digest:      diffID,
			DiffId:      diffID,
			Size:        size,
			Compression: agentpb.RunContainerLayerHeader_COMPRESSION_NONE,
			ChunkHashes: orderedHashes,
		})
	}

	return headers, nil
}
