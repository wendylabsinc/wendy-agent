package containerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
)

// stagedSource returns a chunkSource backed by an in-memory map, mirroring how
// AssembleLayerFromChunks resolves staged/indexed chunks to bytes.
func stagedSource(m map[[32]byte][]byte) chunkSource {
	return func(h [32]byte) ([]byte, error) {
		if b, ok := m[h]; ok {
			return b, nil
		}
		return nil, nil
	}
}

func TestChunkStreamReassembles(t *testing.T) {
	full := bytes.Repeat([]byte("wendy-layer-"), 50_000) // ~600 KiB, multi-chunk
	refs, err := chunk.Chunk(bytes.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}

	src := map[[32]byte][]byte{}
	var order [][32]byte
	off := 0
	for _, r := range refs {
		src[r.Hash] = full[off : off+int(r.Len)]
		order = append(order, r.Hash)
		off += int(r.Len)
	}

	got, err := io.ReadAll(&chunkStream{order: order, src: stagedSource(src)})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Fatalf("reassembled bytes differ (len %d vs %d)", len(got), len(full))
	}
	if sha256.Sum256(got) != sha256.Sum256(full) {
		t.Fatal("digest mismatch")
	}
}

func TestChunkStreamDetectsCorruptChunk(t *testing.T) {
	good := []byte("good-chunk-bytes")
	h := sha256.Sum256(good)
	// Source returns bytes that do not match the requested hash.
	src := func([32]byte) ([]byte, error) { return []byte("tampered"), nil }

	_, err := io.ReadAll(&chunkStream{order: [][32]byte{h}, src: src})
	if err == nil {
		t.Fatal("expected hash-mismatch error for tampered chunk")
	}
}

func TestChunkStreamReportsMissingChunk(t *testing.T) {
	h := sha256.Sum256([]byte("absent"))
	src := func([32]byte) ([]byte, error) { return nil, nil } // unavailable

	_, err := io.ReadAll(&chunkStream{order: [][32]byte{h}, src: src})
	if err == nil {
		t.Fatal("expected error for unavailable chunk")
	}
}

func TestStageChunkRejectsOversizedChunk(t *testing.T) {
	c := &Client{staging: newStaging(t.TempDir())}
	big := bytes.Repeat([]byte{0xab}, maxStagedChunkBytes+1)
	err := c.StageChunk(context.Background(), sha256.Sum256(big), big)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted for oversized chunk, got %v", err)
	}
}

func TestStageChunkRejectsHashMismatch(t *testing.T) {
	c := &Client{staging: newStaging(t.TempDir())}
	data := []byte("real-bytes")
	wrong := sha256.Sum256([]byte("something-else"))
	if err := c.StageChunk(context.Background(), wrong, data); err == nil {
		t.Fatal("expected error when data does not match the claimed hash")
	}
	if c.staging.has(wrong) {
		t.Fatal("a hash-mismatched chunk must not be staged")
	}
}

func TestStagingWriteIsDiskBackedAndIdempotent(t *testing.T) {
	s := newStaging(t.TempDir())
	data := []byte("chunk-payload")
	h := sha256.Sum256(data)

	if s.has(h) {
		t.Fatal("chunk should not exist before staging")
	}
	if err := s.write(h, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !s.has(h) {
		t.Fatal("chunk should exist on disk after staging")
	}
	if n, ok := s.statLen(h); !ok || n != int64(len(data)) {
		t.Fatalf("statLen = (%d, %v), want (%d, true)", n, ok, len(data))
	}
	got, err := s.read(h)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read = (%q, %v), want %q", got, err, data)
	}

	// Re-staging the same chunk is a no-op and must not error.
	if err := s.write(h, data); err != nil {
		t.Fatalf("idempotent re-write: %v", err)
	}

	s.remove(h)
	if s.has(h) {
		t.Fatal("chunk should be gone after remove")
	}
	// Removing an absent chunk is safe.
	s.remove(h)
}
