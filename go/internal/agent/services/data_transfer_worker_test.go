package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// fakeIngestServer is an in-process DataIngestService used to drive the transfer
// worker. It records received bytes per path, verifies SHA-256 on commit, and
// can be configured to report resume offsets or force verification failures.
type fakeIngestServer struct {
	cloudpb.UnimplementedDataIngestServiceServer

	mu sync.Mutex

	expected  map[string]string // path -> expected sha256 (from Begin manifest)
	sizes     map[string]int64  // path -> declared size
	received  map[string][]byte // path -> bytes streamed this session
	committed map[string]int64  // path -> durable offset

	presetCommitted map[string]int64 // returned by Begin (resume simulation)
	corruptPaths    map[string]bool  // force verification failure for these paths
	beginState      cloudpb.EpisodeState

	beginCalls  int
	commitCalls int
}

func newFakeIngestServer() *fakeIngestServer {
	return &fakeIngestServer{
		expected:        map[string]string{},
		sizes:           map[string]int64{},
		received:        map[string][]byte{},
		committed:       map[string]int64{},
		presetCommitted: map[string]int64{},
		corruptPaths:    map[string]bool{},
		beginState:      cloudpb.EpisodeState_EPISODE_STATE_UPLOADING,
	}
}

func (s *fakeIngestServer) BeginEpisodeUpload(_ context.Context, req *cloudpb.BeginEpisodeUploadRequest) (*cloudpb.BeginEpisodeUploadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beginCalls++
	var files []*cloudpb.FileUploadState
	for _, f := range req.GetManifest().GetFiles() {
		s.expected[f.GetPath()] = f.GetSha256()
		s.sizes[f.GetPath()] = int64(f.GetSizeBytes())
		off := s.presetCommitted[f.GetPath()]
		if off > 0 {
			s.committed[f.GetPath()] = off
		}
		files = append(files, &cloudpb.FileUploadState{Path: f.GetPath(), CommittedOffset: off})
	}
	return &cloudpb.BeginEpisodeUploadResponse{State: s.beginState, Files: files}, nil
}

func (s *fakeIngestServer) UploadEpisodeChunk(stream grpc.BidiStreamingServer[cloudpb.EpisodeChunk, cloudpb.EpisodeChunkAck]) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.received[chunk.GetPath()] = append(s.received[chunk.GetPath()], chunk.GetData()...)
		recv := int64(len(s.received[chunk.GetPath()]))
		var committed int64
		if chunk.GetEof() {
			s.committed[chunk.GetPath()] = recv
			committed = recv
		}
		s.mu.Unlock()
		if err := stream.Send(&cloudpb.EpisodeChunkAck{Path: chunk.GetPath(), ReceivedOffset: recv, CommittedOffset: committed}); err != nil {
			return err
		}
	}
}

func (s *fakeIngestServer) CommitEpisode(_ context.Context, _ *cloudpb.CommitEpisodeRequest) (*cloudpb.CommitEpisodeResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitCalls++
	var files []*cloudpb.FileVerification
	allOK := true
	for path, exp := range s.expected {
		var actual string
		if b, ok := s.received[path]; ok {
			sum := sha256.Sum256(b)
			actual = hex.EncodeToString(sum[:])
		} else if s.committed[path] >= s.sizes[path] {
			// Already durably stored from a prior session; not re-sent.
			actual = exp
		}
		ok := actual == exp
		detail := ""
		if s.corruptPaths[path] {
			ok = false
			actual = "0000000000000000000000000000000000000000000000000000000000000000"
			detail = "forced checksum mismatch"
		}
		if !ok {
			allOK = false
			if detail == "" {
				detail = "checksum mismatch"
			}
		}
		files = append(files, &cloudpb.FileVerification{Path: path, Ok: ok, ExpectedSha256: exp, ActualSha256: actual, Detail: detail})
	}
	state := cloudpb.EpisodeState_EPISODE_STATE_COMPLETE
	if !allOK {
		state = cloudpb.EpisodeState_EPISODE_STATE_FAILED
	}
	return &cloudpb.CommitEpisodeResponse{State: state, Files: files}, nil
}

// startFakeIngest registers the fake on a bufconn gRPC server and returns a
// connected client plus a cleanup function.
func startFakeIngest(t *testing.T, srv *fakeIngestServer) cloudpb.DataIngestServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	g := grpc.NewServer()
	cloudpb.RegisterDataIngestServiceServer(g, srv)
	go func() { _ = g.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		g.Stop()
		lis.Close()
	})
	return cloudpb.NewDataIngestServiceClient(conn)
}

// newTestWorker builds a worker wired to the given manager and fake ingest
// client, with real-time pacing/backoff by default.
func newTestWorker(mgr *data.Manager, client cloudpb.DataIngestServiceClient) *DataTransferWorker {
	w := &DataTransferWorker{
		logger:      zap.NewNop(),
		manager:     mgr,
		maxAttempts: transferMaxAttempts,
		now:         time.Now,
		newSleeper:  contextSleeper,
	}
	w.factory = func(context.Context) (cloudpb.DataIngestServiceClient, uint64, uint64, func(), error) {
		return client, 7, 42, func() {}, nil
	}
	return w
}

// writeFixtureEpisode creates a finalized episode directory with the given files
// and upload state. Returns the computed total size.
func writeFixtureEpisode(t *testing.T, root, id, campaign, uploadState string, files map[string][]byte) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir episode: %v", err)
	}
	var manifestFiles []data.File
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir file parent: %v", err)
		}
		if err := os.WriteFile(full, content, 0o640); err != nil {
			t.Fatalf("write file: %v", err)
		}
		sum := sha256.Sum256(content)
		manifestFiles = append(manifestFiles, data.File{
			Path:      path,
			Size:      int64(len(content)),
			SHA256:    hex.EncodeToString(sum[:]),
			Format:    "bin",
			MediaType: "application/octet-stream",
		})
	}
	mf := data.Manifest{
		Version:          data.ManifestVersion,
		ID:               id,
		State:            "complete",
		StartedUnixNanos: time.Now().UnixNano(),
		Trigger:          data.EpisodeTrigger{Reason: "test", CampaignName: campaign},
		Files:            manifestFiles,
		Upload:           data.WorkflowState{State: uploadState},
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o640); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// writeCampaign writes a campaign plan JSON the manager can read.
func writeCampaign(t *testing.T, root, name, when, maxRate string) {
	t.Helper()
	dir := filepath.Join(filepath.Dir(root), "campaigns")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir campaigns: %v", err)
	}
	c := data.Campaign{
		Version: 1,
		Name:    name,
		Upload:  data.CampaignUpload{When: when, MaxRate: maxRate},
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("marshal campaign: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o640); err != nil {
		t.Fatalf("write campaign: %v", err)
	}
}

func newTestManager(t *testing.T) (*data.Manager, string) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "episodes")
	mgr, err := data.NewManager(root)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return mgr, root
}

func uploadState(t *testing.T, mgr *data.Manager, id string) data.WorkflowState {
	t.Helper()
	mf, _, err := mgr.Inspect(id, false)
	if err != nil {
		t.Fatalf("Inspect %s: %v", id, err)
	}
	return mf.Upload
}

func TestDataTransferWorker_HappyPath(t *testing.T) {
	mgr, root := newTestManager(t)
	fileA := []byte("hello wendy data platform")
	fileB := make([]byte, 3*1024) // multi-KiB, still one chunk
	for i := range fileB {
		fileB[i] = byte(i)
	}
	files := map[string][]byte{"video/000001.jpg": fileA, "meta.json": fileB}
	writeFixtureEpisode(t, root, "ep-happy", "", "pending", files)

	srv := newFakeIngestServer()
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}

	if got := uploadState(t, mgr, "ep-happy").State; got != uploadStateUploaded {
		t.Fatalf("state = %q, want uploaded", got)
	}
	if srv.beginCalls != 1 || srv.commitCalls != 1 {
		t.Fatalf("begin=%d commit=%d, want 1/1", srv.beginCalls, srv.commitCalls)
	}
	for path, want := range files {
		if got := srv.received[path]; string(got) != string(want) {
			t.Errorf("file %s: received %d bytes, want %d", path, len(got), len(want))
		}
	}
}

func TestDataTransferWorker_ResumeSkipsCommittedFiles(t *testing.T) {
	mgr, root := newTestManager(t)
	done := []byte("already uploaded before the crash")
	pending := []byte("still needs uploading")
	files := map[string][]byte{"done.bin": done, "pending.bin": pending}
	// Episode left "uploading" by a crashed worker.
	writeFixtureEpisode(t, root, "ep-resume", "", "uploading", files)

	srv := newFakeIngestServer()
	// Server already has done.bin fully (file-granularity committed offset).
	srv.presetCommitted["done.bin"] = int64(len(done))
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}

	if got := uploadState(t, mgr, "ep-resume").State; got != uploadStateUploaded {
		t.Fatalf("state = %q, want uploaded", got)
	}
	if len(srv.received["done.bin"]) != 0 {
		t.Errorf("done.bin re-uploaded (%d bytes); resume must skip committed files", len(srv.received["done.bin"]))
	}
	if string(srv.received["pending.bin"]) != string(pending) {
		t.Errorf("pending.bin not fully uploaded")
	}
	if srv.beginCalls != 1 {
		t.Errorf("begin called %d times, want 1 (idempotent)", srv.beginCalls)
	}
}

func TestDataTransferWorker_BandwidthCeilingPaces(t *testing.T) {
	mgr, root := newTestManager(t)
	const size = 128 * 1024 // 128 KiB, one chunk
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i)
	}
	writeFixtureEpisode(t, root, "ep-paced", "paced-campaign", "pending", map[string][]byte{"blob.bin": content})
	// 256 KiB/s ceiling -> 128 KiB should take ~0.5s.
	writeCampaign(t, root, "paced-campaign", "always", "262144")

	campaign, err := mgr.Campaign("paced-campaign")
	if err != nil {
		t.Fatalf("Campaign: %v", err)
	}
	rate := campaign.UploadMaxRateBytes()
	if rate <= 0 {
		t.Fatalf("campaign rate = %d, want > 0", rate)
	}
	expected := time.Duration(float64(size) / float64(rate) * float64(time.Second))

	srv := newFakeIngestServer()
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	start := time.Now()
	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}
	elapsed := time.Since(start)

	if got := uploadState(t, mgr, "ep-paced").State; got != uploadStateUploaded {
		t.Fatalf("state = %q, want uploaded", got)
	}
	if elapsed < time.Duration(float64(expected)*0.8) {
		t.Errorf("elapsed %v too fast; ceiling not paced (expected ~%v)", elapsed, expected)
	}
	if elapsed > expected*5 {
		t.Errorf("elapsed %v far exceeds expected %v", elapsed, expected)
	}
}

func TestDataTransferWorker_VerificationFailureMarksFailedNoRetry(t *testing.T) {
	mgr, root := newTestManager(t)
	writeFixtureEpisode(t, root, "ep-corrupt", "", "pending", map[string][]byte{"blob.bin": []byte("corrupt me")})

	srv := newFakeIngestServer()
	srv.corruptPaths["blob.bin"] = true
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}
	st := uploadState(t, mgr, "ep-corrupt")
	if st.State != uploadStateFailed {
		t.Fatalf("state = %q, want failed", st.State)
	}
	if st.LastError == "" {
		t.Errorf("failure reason not recorded")
	}

	// Second pass must NOT retry a failed episode.
	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("second runPass: %v", err)
	}
	if srv.beginCalls != 1 {
		t.Errorf("begin called %d times; failed episode must not be retried", srv.beginCalls)
	}
}

func TestDataTransferWorker_UploadWhenManualSkipped_AlwaysTaken(t *testing.T) {
	mgr, root := newTestManager(t)
	writeFixtureEpisode(t, root, "ep-manual", "manual-campaign", "pending", map[string][]byte{"a.bin": []byte("a")})
	writeFixtureEpisode(t, root, "ep-always", "always-campaign", "pending", map[string][]byte{"b.bin": []byte("b")})
	writeCampaign(t, root, "manual-campaign", "manual", "")
	writeCampaign(t, root, "always-campaign", "always", "")

	srv := newFakeIngestServer()
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}

	if got := uploadState(t, mgr, "ep-manual").State; got != uploadStatePending {
		t.Errorf("manual episode state = %q, want pending (skipped)", got)
	}
	if got := uploadState(t, mgr, "ep-always").State; got != uploadStateUploaded {
		t.Errorf("always episode state = %q, want uploaded", got)
	}
	if _, ok := srv.received["a.bin"]; ok {
		t.Errorf("manual episode file was uploaded")
	}
}

func TestDataTransferWorker_AtLeastOnceDuplicateSafe(t *testing.T) {
	mgr, root := newTestManager(t)
	files := map[string][]byte{"x.bin": []byte("payload")}
	writeFixtureEpisode(t, root, "ep-replay", "", "uploading", files)

	srv := newFakeIngestServer()
	// Simulate a prior fully-delivered upload the worker crashed after: Begin
	// reports the episode already COMPLETE. A re-run must not re-stream bytes.
	srv.beginState = cloudpb.EpisodeState_EPISODE_STATE_COMPLETE
	client := startFakeIngest(t, srv)
	w := newTestWorker(mgr, client)

	if err := w.runPass(context.Background()); err != nil {
		t.Fatalf("runPass: %v", err)
	}
	if got := uploadState(t, mgr, "ep-replay").State; got != uploadStateUploaded {
		t.Fatalf("state = %q, want uploaded", got)
	}
	if len(srv.received["x.bin"]) != 0 {
		t.Errorf("bytes re-streamed on idempotent replay: %d", len(srv.received["x.bin"]))
	}
	if srv.commitCalls != 0 {
		t.Errorf("commit called %d times on already-complete replay, want 0", srv.commitCalls)
	}
}
