package containerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
)

// maxStagedChunkBytes bounds a single staged chunk. The CDC chunker emits
// chunks of at most chunk.MaxSize (256 KiB); this 4 MiB ceiling leaves ample
// headroom for legitimate clients while rejecting absurdly large payloads.
const maxStagedChunkBytes = 4 << 20

// defaultChunkStagingDir is where chunks received via WriteChunks are buffered
// until AssembleLayerFromChunks consumes them. It is deliberately disk-backed
// (not an in-memory map) so reassembling a multi-GiB layer — e.g. LLM weights
// on a Jetson AGX Thor — does not consume device RAM that belongs to the apps.
const defaultChunkStagingDir = "/var/lib/wendy/chunk-staging"

// staging persists chunk bytes received via StageChunk to disk, keyed by hash,
// until the next AssembleLayerFromChunks streams them into the content store.
// Disk — not the agent heap — is the staging budget, so peak memory during a
// deploy is one chunk rather than the whole uncompressed layer (×2 with the old
// reconstruct buffer). Distinct chunks map to distinct files, so concurrent
// deploys staging different content do not collide.
type staging struct {
	dir string
}

func newStaging(dir string) *staging { return &staging{dir: dir} }

// path returns the on-disk location for a chunk hash.
func (s *staging) path(h [32]byte) string {
	return filepath.Join(s.dir, hex.EncodeToString(h[:]))
}

// has reports whether the chunk is already staged on disk.
func (s *staging) has(h [32]byte) bool {
	_, err := os.Stat(s.path(h))
	return err == nil
}

// read returns the staged chunk bytes, or an os.IsNotExist error if absent.
func (s *staging) read(h [32]byte) ([]byte, error) {
	return os.ReadFile(s.path(h))
}

// statLen returns the staged chunk's size without reading it, false if absent.
func (s *staging) statLen(h [32]byte) (int64, bool) {
	fi, err := os.Stat(s.path(h))
	if err != nil {
		return 0, false
	}
	return fi.Size(), true
}

// write stores data for hash h atomically (temp file + rename), creating the
// staging directory on demand. It is idempotent: a chunk already on disk is
// left untouched. Files are written 0600 — readable only by the agent user.
func (s *staging) write(h [32]byte, data []byte) error {
	if s.has(h) {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, "stage-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path(h)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// remove deletes a staged chunk, ignoring a missing file.
func (s *staging) remove(h [32]byte) {
	_ = os.Remove(s.path(h))
}

// chunkSource resolves a chunk hash to its raw bytes, returning (nil, nil) when
// the chunk is available from no source so the caller can report it as missing.
type chunkSource func(h [32]byte) ([]byte, error)

// chunkStream is an io.Reader that yields the chunks named by order in sequence,
// holding at most one chunk in memory at a time and verifying each chunk's
// SHA-256 as it is served. Feeding it to content.WriteBlob reassembles a layer
// without ever buffering the whole layer in RAM; WriteBlob independently
// verifies the overall layer digest as it streams.
type chunkStream struct {
	order [][32]byte
	src   chunkSource
	idx   int
	cur   *bytes.Reader
}

func (s *chunkStream) Read(p []byte) (int, error) {
	for {
		if s.cur != nil {
			n, err := s.cur.Read(p)
			if err == io.EOF {
				s.cur = nil
				continue
			}
			return n, err
		}
		if s.idx >= len(s.order) {
			return 0, io.EOF
		}
		h := s.order[s.idx]
		s.idx++
		b, err := s.src(h)
		if err != nil {
			return 0, fmt.Errorf("chunk %d: %w", s.idx-1, err)
		}
		if b == nil {
			return 0, fmt.Errorf("chunk %d (%x) unavailable", s.idx-1, h)
		}
		if sha256.Sum256(b) != h {
			return 0, fmt.Errorf("chunk %d hash mismatch", s.idx-1)
		}
		s.cur = bytes.NewReader(b)
	}
}

func (c *Client) MissingChunks(_ context.Context, hashes [][32]byte) ([][32]byte, error) {
	missing := c.chunkIndex.Missing(hashes)
	// Exclude any already staged on disk (possibly from an earlier session).
	var out [][32]byte
	for _, h := range missing {
		if !c.staging.has(h) {
			out = append(out, h)
		}
	}
	return out, nil
}

func (c *Client) StageChunk(_ context.Context, h [32]byte, data []byte) error {
	if len(data) > maxStagedChunkBytes {
		return status.Errorf(codes.ResourceExhausted, "chunk too large: %d > %d bytes", len(data), maxStagedChunkBytes)
	}
	if sha256.Sum256(data) != h {
		return fmt.Errorf("staged chunk hash mismatch")
	}
	if err := c.staging.write(h, data); err != nil {
		return fmt.Errorf("staging chunk to disk: %w", err)
	}
	return nil
}

// readIndexedChunk reads a chunk's bytes from the uncompressed blob it was
// indexed into. Returns (nil,nil) when the blob is gone so the caller can treat
// it as a miss and prune the stale entry.
func (c *Client) readIndexedChunk(ctx context.Context, loc chunkLoc) ([]byte, error) {
	cs := c.client.ContentStore()
	dgst, err := digest.Parse(loc.Blob)
	if err != nil {
		return nil, err
	}
	ra, err := cs.ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if err != nil {
		if errdefs.IsNotFound(err) {
			c.chunkIndex.Drop(loc.Blob)
			return nil, nil
		}
		return nil, err
	}
	defer ra.Close()
	b := make([]byte, loc.Len)
	if _, err := ra.ReadAt(b, int64(loc.Offset)); err != nil {
		return nil, err
	}
	return b, nil
}

// chunkLen returns a chunk's length from its staged file or its indexed blob
// range, without reading the bytes. ok is false when the chunk is unavailable.
func (c *Client) chunkLen(h [32]byte) (int64, bool) {
	if n, ok := c.staging.statLen(h); ok {
		return n, true
	}
	if loc, ok := c.chunkIndex.Has(h); ok {
		return int64(loc.Len), true
	}
	return 0, false
}

func (c *Client) AssembleLayerFromChunks(ctx context.Context, diffID string, hashes [][32]byte) error {
	nsCtx := c.withNamespace(ctx)

	// Fast path: if the (uncompressed) layer blob already exists in the content
	// store, it was reassembled and indexed on a previous deploy. Skip the
	// expensive reconstruct + re-chunk + index-save entirely — for an unchanged
	// layer this avoids reading and re-chunking the full layer on every deploy,
	// which dominates redeploy latency for large base images.
	if dgst, err := digest.Parse(diffID); err == nil {
		if _, err := c.client.ContentStore().Info(nsCtx, dgst); err == nil {
			return nil
		}
	}

	// Total layer size is the sum of the chunk lengths, resolved without reading
	// any bytes. content.WriteBlob needs the size up front to commit the blob.
	var total int64
	for i, h := range hashes {
		n, ok := c.chunkLen(h)
		if !ok {
			return fmt.Errorf("chunk %d (%x) unavailable", i, h)
		}
		total += n
	}

	// Stream the chunks straight into the content store. WriteBlob verifies the
	// reassembled bytes hash to diffID as it writes (so a corrupt or forged
	// stream fails the commit), which subsumes the old whole-buffer digest check.
	src := func(h [32]byte) ([]byte, error) {
		if b, err := c.staging.read(h); err == nil {
			return b, nil
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if loc, ok := c.chunkIndex.Has(h); ok {
			return c.readIndexedChunk(nsCtx, loc)
		}
		return nil, nil
	}
	stream := &chunkStream{order: hashes, src: src}
	if err := c.WriteLayer(nsCtx, diffID, stream, total); err != nil {
		return err
	}

	// Re-chunk the freshly written blob by streaming it back out of the content
	// store, so the index references this blob (offsets relative to it) without
	// holding the layer in memory.
	if err := c.indexLayerBlob(nsCtx, diffID); err != nil {
		c.logger.Warn("failed to index reassembled layer", zap.String("diff_id", diffID), zap.Error(err))
	}

	// Release the staged chunks now embedded in the blob.
	for _, h := range hashes {
		c.staging.remove(h)
	}

	return nil
}

// indexLayerBlob re-chunks the layer blob identified by diffID by streaming it
// from the content store, and records the chunk ranges in the persistent index.
func (c *Client) indexLayerBlob(ctx context.Context, diffID string) error {
	dgst, err := digest.Parse(diffID)
	if err != nil {
		return err
	}
	ra, err := c.client.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: dgst})
	if err != nil {
		return err
	}
	defer ra.Close()

	refs, err := chunk.ChunkReaderAt(ra, ra.Size())
	if err != nil {
		return err
	}
	c.chunkIndex.AddLayer(diffID, refs)
	if err := c.chunkIndex.Save(); err != nil {
		c.logger.Warn("failed to persist chunk index", zap.Error(err))
	}
	return nil
}
