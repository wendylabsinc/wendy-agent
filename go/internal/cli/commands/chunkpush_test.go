package commands

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPushLayersByChunksRoutesStatusToOutput(t *testing.T) {
	diffID := "sha256:" + strings.Repeat("ab", 32)
	fake := &fakeContainerClient{
		queryFn: func(*agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			return &agentpb.QueryChunksResponse{}
		},
		queryLayersFn: func(*agentpb.QueryLayersRequest) *agentpb.QueryLayersResponse {
			return &agentpb.QueryLayersResponse{
				Present: []*agentpb.PresentLayer{{DiffId: diffID, Size: 4096}},
			}
		},
	}
	var output bytes.Buffer
	_, err := pushLayersByChunksWithPrepareOutput(context.Background(), fake, []localLayer{{
		DiffID: diffID,
	}}, nil, &output)
	if err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Reusing 1 layer(s) already on device; chunking 0.") {
		t.Fatalf("routed status = %q", got)
	}
}

// fakeContainerClient satisfies agentpb.WendyContainerServiceClient via the
// embedded-interface trick. Only QueryChunks, QueryLayers, and WriteChunks are
// overridden; all other methods panic (they must not be called by
// pushLayersByChunks).
type fakeContainerClient struct {
	agentpb.WendyContainerServiceClient // embedded nil — satisfies interface
	queryFn                             func(*agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse
	queryLayersFn                       func(*agentpb.QueryLayersRequest) *agentpb.QueryLayersResponse
	writeFn                             func(*agentpb.WriteChunksRequest) error
	chunksWritten                       int
	writeStarted                        chan struct{}
	blockWrites                         bool
	writeOnce                           sync.Once
}

// TestPushLayersByChunksPreparesDuringUpload proves the preparation RPC is
// started after manifests are known but before WriteChunks finishes. This is
// the wall-clock overlap the optimization exists to create.
func TestPushLayersByChunksPreparesDuringUpload(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	layerTar := bytes.Repeat([]byte("prewarm-layer-"), 100_000)
	prepareStarted := make(chan struct{})
	allowPrepare := make(chan struct{})
	var once sync.Once
	fake := &fakeContainerClient{
		queryFn: func(req *agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			if len(req.GetChunkHashes()) == 0 {
				return &agentpb.QueryChunksResponse{}
			}
			return &agentpb.QueryChunksResponse{MissingHashes: req.GetChunkHashes()[:1]}
		},
		writeFn: func(*agentpb.WriteChunksRequest) error {
			<-prepareStarted
			once.Do(func() { close(allowPrepare) })
			return nil
		},
	}
	prepare := func(_ context.Context, headers []*agentpb.RunContainerLayerHeader) error {
		if len(headers) != 1 || len(headers[0].GetChunkHashes()) == 0 {
			t.Fatalf("prepare received incomplete layer manifests: %#v", headers)
		}
		close(prepareStarted)
		<-allowPrepare
		return nil
	}

	headers, err := pushLayersByChunksWithPrepare(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex(layerTar),
		DiffID:    "sha256:" + sha256Hex(layerTar),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      layerTar,
	}}, prepare)
	if err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 {
		t.Fatalf("headers = %d, want 1", len(headers))
	}
}

func TestPushLayersByChunksPrepareUnimplementedFallsBack(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	layerTar := []byte("already available layer")
	fake := &fakeContainerClient{
		queryFn: func(*agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			return &agentpb.QueryChunksResponse{}
		},
	}
	_, err := pushLayersByChunksWithPrepare(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex(layerTar),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      layerTar,
	}}, func(context.Context, []*agentpb.RunContainerLayerHeader) error {
		return status.Error(codes.Unimplemented, "old agent")
	})
	if err != nil {
		t.Fatalf("Unimplemented preparation must fall back to RunContainer, got %v", err)
	}
}

func TestPushLayersByChunksStrictPrepareReturnsUnimplemented(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	layerTar := []byte("already available layer")
	fake := &fakeContainerClient{
		queryFn: func(*agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			return &agentpb.QueryChunksResponse{}
		},
	}
	_, err := pushLayersByChunksWithStrictPrepareOutput(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex(layerTar),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      layerTar,
	}}, func(context.Context, []*agentpb.RunContainerLayerHeader) error {
		return status.Error(codes.Unimplemented, "old agent")
	}, nil)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("strict preparation error = %v, want Unimplemented", err)
	}
}

func TestPushLayersByChunksStrictPrepareCancelsUploadOnPrepareFailure(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	layerTar := bytes.Repeat([]byte("cancel-on-prepare-failure-"), 100_000)
	refs, err := chunk.Chunk(bytes.NewReader(layerTar))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(refs))
	}

	fake := &fakeContainerClient{
		queryFn: func(req *agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			if len(req.GetChunkHashes()) == 0 {
				return &agentpb.QueryChunksResponse{}
			}
			return &agentpb.QueryChunksResponse{MissingHashes: req.GetChunkHashes()}
		},
		writeStarted: make(chan struct{}),
		blockWrites:  true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	_, err = pushLayersByChunksWithStrictPrepareOutput(ctx, fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex(layerTar),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      layerTar,
	}}, func(context.Context, []*agentpb.RunContainerLayerHeader) error {
		<-fake.writeStarted
		return status.Error(codes.Unimplemented, "old agent")
	}, nil)
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("strict preparation error = %v, want Unimplemented", err)
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("prepare failure took %s to cancel chunk upload", elapsed)
	}
	if fake.chunksWritten != 1 {
		t.Fatalf("wrote %d chunks after preparation failed, want 1 in-flight chunk", fake.chunksWritten)
	}
}

func (f *fakeContainerClient) QueryChunks(_ context.Context, in *agentpb.QueryChunksRequest, _ ...grpc.CallOption) (*agentpb.QueryChunksResponse, error) {
	return f.queryFn(in), nil
}

// QueryLayers delegates to queryLayersFn when set; otherwise it reports
// Unimplemented, mirroring an agent too old for the layer pre-check so the push
// degrades to chunking every layer.
func (f *fakeContainerClient) QueryLayers(_ context.Context, in *agentpb.QueryLayersRequest, _ ...grpc.CallOption) (*agentpb.QueryLayersResponse, error) {
	if f.queryLayersFn == nil {
		return nil, status.Error(codes.Unimplemented, "QueryLayers not implemented")
	}
	return f.queryLayersFn(in), nil
}

func (f *fakeContainerClient) WriteChunks(ctx context.Context, _ ...grpc.CallOption) (grpc.ClientStreamingClient[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse], error) {
	return &fakeWriteChunksStream{parent: f, ctx: ctx}, nil
}

// fakeWriteChunksStream satisfies grpc.ClientStreamingClient via embedding.
type fakeWriteChunksStream struct {
	grpc.ClientStreamingClient[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse] // embedded nil
	parent                                                                              *fakeContainerClient
	ctx                                                                                 context.Context
}

func (s *fakeWriteChunksStream) Send(req *agentpb.WriteChunksRequest) error {
	s.parent.chunksWritten++
	if s.parent.writeStarted != nil {
		s.parent.writeOnce.Do(func() { close(s.parent.writeStarted) })
	}
	if s.parent.blockWrites {
		<-s.ctx.Done()
		return s.ctx.Err()
	}
	if s.parent.writeFn != nil {
		return s.parent.writeFn(req)
	}
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

// TestPushLayersByChunksSkipsPresentLayer verifies that a layer the device
// already has (reported by QueryLayers) is never decompressed, chunked, or
// transferred — its header is built from the diff ID and the device-reported
// size alone.
func TestPushLayersByChunksSkipsPresentLayer(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	diffID := "sha256:" + strings.Repeat("ab", 32)
	const presentSize int64 = 4096

	fake := &fakeContainerClient{
		queryFn: func(req *agentpb.QueryChunksRequest) *agentpb.QueryChunksResponse {
			// The only legitimate QueryChunks here is the empty capability probe.
			if len(req.GetChunkHashes()) != 0 {
				t.Errorf("QueryChunks called with %d hashes; a present layer must not be chunked", len(req.GetChunkHashes()))
			}
			return &agentpb.QueryChunksResponse{}
		},
		queryLayersFn: func(req *agentpb.QueryLayersRequest) *agentpb.QueryLayersResponse {
			return &agentpb.QueryLayersResponse{
				Present: []*agentpb.PresentLayer{{DiffId: diffID, Size: presentSize}},
			}
		},
	}

	// The blob is intentionally NOT valid gzip: if the push tried to decompress
	// this layer it would fail, proving the present layer was skipped.
	headers, err := pushLayersByChunks(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex([]byte("compressed-bytes")),
		DiffID:    diffID,
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Blob:      []byte("this is not gzip"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if fake.chunksWritten != 0 {
		t.Fatalf("expected 0 chunks written for a present layer, got %d", fake.chunksWritten)
	}
	if len(headers) != 1 {
		t.Fatalf("expected 1 header, got %d", len(headers))
	}
	h := headers[0]
	if h.GetDiffId() != diffID || h.GetDigest() != diffID {
		t.Fatalf("present-layer header digest/diffID mismatch: digest=%q diffID=%q want %q", h.GetDigest(), h.GetDiffId(), diffID)
	}
	if h.GetSize() != presentSize {
		t.Fatalf("present-layer header size = %d, want %d (device-reported)", h.GetSize(), presentSize)
	}
	if len(h.GetChunkHashes()) != 0 {
		t.Fatalf("present-layer header must carry no chunk hashes, got %d", len(h.GetChunkHashes()))
	}
	if h.GetCompression() != agentpb.RunContainerLayerHeader_COMPRESSION_NONE {
		t.Fatalf("present-layer header must be uncompressed, got %v", h.GetCompression())
	}
}

// TestPushLayersByChunksProbeUnimplemented verifies that an agent which does not
// support chunk-diff at all (QueryChunks returns Unimplemented) surfaces the
// error before any layer work, so the caller can fall back to a registry push.
func TestPushLayersByChunksProbeUnimplemented(t *testing.T) {
	manifestCacheTestDir = t.TempDir()
	t.Cleanup(func() { manifestCacheTestDir = "" })

	fake := &probeUnsupportedClient{}
	_, err := pushLayersByChunks(context.Background(), fake, []localLayer{{
		Digest:    "sha256:" + sha256Hex([]byte("x")),
		MediaType: "application/vnd.oci.image.layer.v1.tar",
		Blob:      []byte("not gzip either"),
	}})
	if !isUnimplementedRPCError(err) {
		t.Fatalf("expected Unimplemented error from the capability probe, got %v", err)
	}
}

// probeUnsupportedClient fails QueryChunks with Unimplemented, modelling an agent
// too old for any chunk-diff support.
type probeUnsupportedClient struct {
	agentpb.WendyContainerServiceClient
}

func (probeUnsupportedClient) QueryChunks(_ context.Context, _ *agentpb.QueryChunksRequest, _ ...grpc.CallOption) (*agentpb.QueryChunksResponse, error) {
	return nil, status.Error(codes.Unimplemented, "QueryChunks not implemented")
}
