package commands

import (
	"bytes"
	"context"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
)

// fakeContainerClient satisfies agentpb.WendyContainerServiceClient via the
// embedded-interface trick. Only QueryChunks and WriteChunks are overridden;
// all other methods panic (they must not be called by pushLayersByChunks).
type fakeContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface
	queryFn                             func(*agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse
	chunksWritten                       int
}

func (f *fakeContainerClient) QueryChunks(_ context.Context, in *agentpb.QueryChunksRequest, _ ...grpc.CallOption) (*agentpb.QueryChunksResponse, error) {
	return f.queryFn(in), nil
}

func (f *fakeContainerClient) WriteChunks(_ context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse], error) {
	return &fakeWriteChunksStream{parent: f}, nil
}

// fakeWriteChunksStream satisfies grpc.ClientStreamingClient via embedding.
type fakeWriteChunksStream struct {
	grpc.ClientStreamingClient[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse] // embedded nil
	parent                                                                              *fakeContainerClient
}

func (s *fakeWriteChunksStream) Send(_ *agentpb.WriteChunksRequest) error {
	s.parent.chunksWritten++
	return nil
}

func (s *fakeWriteChunksStream) CloseAndRecv() (*agentpb.WriteChunksResponse, error) {
	return &agentpb.WriteChunksResponse{}, nil
}

func TestPushLayersByChunksWritesOnlyMissing(t *testing.T) {
	// Isolate the manifest cache so the test neither reads nor pollutes the
	// real user cache (and starts from a guaranteed cache miss).
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	layerTar := bytes.Repeat([]byte("abc"), 300_000) // multi-chunk
	refs, err := chunk.Chunk(bytes.NewReader(layerTar))
	if err != nil {
		t.Fatalf("chunk.Chunk: %v", err)
	}
	if len(refs) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(refs))
	}

	// Fake device already has every chunk except the first.
	have := map[[32]byte]bool{}
	for _, r := range refs[1:] {
		have[r.Hash] = true
	}
	fake := &fakeContainerClient{
		queryFn: func(req *agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			var missing [][]byte
			for _, hb := range req.GetChunkHashes() {
				var h [32]byte
				copy(h[:], hb)
				if !have[h] {
					missing = append(missing, hb)
				}
			}
			return &agentpb.QueryChunksResponse{MissingHashes: missing}
		},
	}
	// An uncompressed layer: the blob bytes are the raw tar, so decompress()
	// returns them as-is. Digest is the (compressed==uncompressed) blob digest.
	headers, err := pushLayersByChunks(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex(layerTar),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      layerTar,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.chunksWritten; got != 1 {
		t.Fatalf("expected exactly 1 chunk written, got %d", got)
	}
	if len(headers) != 1 || len(headers[0].GetChunkHashes()) != len(refs) {
		t.Fatalf("header manifest wrong: got %d headers, chunk hashes %d (want %d)", len(headers), func() int {
			if len(headers) > 0 {
				return len(headers[0].GetChunkHashes())
			}
			return 0
		}(), len(refs))
	}
	if headers[0].GetCompression() != agentpb.RunContainerLayerHeader_COMPRESSION_NONE {
		t.Fatalf("layer must be uncompressed, got %v", headers[0].GetCompression())
	}
}
