package services

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// fileSyncStream replays a scripted client into the service and records what
// the service sent back.
type fileSyncStream struct {
	grpc.ServerStream
	msgs []*agentpbv2.FileSyncRequest
	pos  int
	sent []*agentpbv2.FileSyncResponse
}

func (s *fileSyncStream) Recv() (*agentpbv2.FileSyncRequest, error) {
	if s.pos >= len(s.msgs) {
		return nil, io.EOF
	}
	m := s.msgs[s.pos]
	s.pos++
	return m, nil
}

func (s *fileSyncStream) Send(r *agentpbv2.FileSyncResponse) error {
	s.sent = append(s.sent, r)
	return nil
}

// newTestService points the service at a temp dir standing in for
// /var/lib/wendy/volumes and reports plenty of free space unless a test says
// otherwise.
func newTestService(t *testing.T) (*FileSyncServiceV2, string) {
	t.Helper()
	dir := t.TempDir()
	svc := &FileSyncServiceV2{
		logger:     zap.NewNop(),
		volumesDir: dir,
		availBytes: func(string) (int64, bool) { return 1 << 40, true },
	}
	return svc, dir
}

func makeVolume(t *testing.T, volumesDir, name string) string {
	t.Helper()
	path := filepath.Join(volumesDir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("creating volume: %v", err)
	}
	return path
}

func entryFor(path string, data []byte, mode uint32) *agentpbv2.FileSyncEntry {
	sum := sha256.Sum256(data)
	return &agentpbv2.FileSyncEntry{
		Path:   path,
		Size:   int64(len(data)),
		Sha256: sum[:],
		Mode:   mode,
	}
}

// pushScript is the message sequence a client sends to push one file in a
// single chunk.
func pushScript(volume, prefix, path string, data []byte, mode uint32) []*agentpbv2.FileSyncRequest {
	entry := entryFor(path, data, mode)
	sum := sha256.Sum256(data)
	return []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     volume,
			PathPrefix: prefix,
			Manifest:   &agentpbv2.FileSyncManifest{Files: []*agentpbv2.FileSyncEntry{entry}},
		}}},
		{RequestType: &agentpbv2.FileSyncRequest_Chunk{Chunk: &agentpbv2.FileSyncChunk{
			Path:           path,
			Data:           data,
			Sequence:       0,
			CumulativeSize: int64(len(data)),
			Sha256:         sum[:],
		}}},
		{RequestType: &agentpbv2.FileSyncRequest_Commit{Commit: &agentpbv2.FileSyncCommit{
			Path:   path,
			Sha256: sum[:],
			Size:   int64(len(data)),
		}}},
	}
}

func runStream(t *testing.T, svc *FileSyncServiceV2, msgs []*agentpbv2.FileSyncRequest) (*fileSyncStream, error) {
	t.Helper()
	stream := &fileSyncStream{msgs: msgs}
	return stream, svc.SyncFiles(stream)
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil error", want)
	}
	if got := status.Code(err); got != want {
		t.Fatalf("expected %s, got %s (%v)", want, got, err)
	}
}

func TestPushWritesFileIntoVolume(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "edge-detector-data")

	data := []byte("onnx-model-bytes")
	_, err := runStream(t, svc, pushScript("edge-detector-data", "models", "sffa_yolo.onnx", data, 0o644))
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(volume, "models", "sffa_yolo.onnx"))
	if err != nil {
		t.Fatalf("reading pushed file: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("content mismatch: got %q, want %q", got, data)
	}
}

// A pushed artifact must be readable by the app that mounts the volume, which
// runs as a different user than the agent.
func TestPushAppliesManifestMode(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	_, err := runStream(t, svc, pushScript("data", "", "model.bin", []byte("x"), 0o644))
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	info, err := os.Stat(filepath.Join(volume, "model.bin"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("mode: got %o, want 644", perm)
	}
}

// The manifest the agent replies with is what lets the client skip a file it
// already pushed.
func TestStartReturnsManifestOfExistingFiles(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	existing := []byte("already here")
	if err := os.WriteFile(filepath.Join(volume, "old.bin"), existing, 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	stream, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{},
		}}},
	})
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	manifest := stream.sent[0].GetManifest()
	if manifest == nil || len(manifest.GetFiles()) != 1 {
		t.Fatalf("expected 1 manifest entry, got %+v", stream.sent[0])
	}
	entry := manifest.GetFiles()[0]
	want := sha256.Sum256(existing)
	if entry.GetPath() != "old.bin" || string(entry.GetSha256()) != string(want[:]) {
		t.Fatalf("unexpected manifest entry: %+v", entry)
	}
}

// A push into a volume of models must not report the other models as stale, or
// a mirroring client would delete them.
func TestPathPrefixScopesManifest(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	if err := os.WriteFile(filepath.Join(volume, "unrelated.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	stream, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     "data",
			PathPrefix: "models",
			Manifest:   &agentpbv2.FileSyncManifest{},
		}}},
	})
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if files := stream.sent[0].GetManifest().GetFiles(); len(files) != 0 {
		t.Fatalf("expected empty scoped manifest, got %+v", files)
	}
}

func TestCorruptChunkIsRejectedAndLeavesNoFile(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	data := []byte("good bytes")
	msgs := pushScript("data", "", "model.bin", data, 0o644)
	// Flip the payload after the manifest declared its digest — the shape a
	// silently corrupted transfer takes.
	chunk := msgs[1].GetChunk()
	chunk.Data = []byte("bad bytes!")

	_, err := runStream(t, svc, msgs)
	requireCode(t, err, codes.DataLoss)

	entries, readErr := os.ReadDir(volume)
	if readErr != nil {
		t.Fatalf("reading volume: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files left behind, got %d", len(entries))
	}
}

// A client that dies mid-file must not leave a temp file behind, and must
// never leave a partial file at the destination.
func TestInterruptedTransferCleansUp(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	data := []byte("first half second half")
	entry := entryFor("model.bin", data, 0o644)
	half := data[:10]
	sum := sha256.Sum256(half)

	// Start and one chunk, then EOF with no commit.
	_, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{Files: []*agentpbv2.FileSyncEntry{entry}},
		}}},
		{RequestType: &agentpbv2.FileSyncRequest_Chunk{Chunk: &agentpbv2.FileSyncChunk{
			Path:           "model.bin",
			Data:           half,
			CumulativeSize: int64(len(half)),
			Sha256:         sum[:],
		}}},
	})
	requireCode(t, err, codes.InvalidArgument)

	entries, readErr := os.ReadDir(volume)
	if readErr != nil {
		t.Fatalf("reading volume: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no leftovers, got %v", entries)
	}
}

func TestPathTraversalIsRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	for _, path := range []string{"../escape.bin", "models/../../escape.bin", "/etc/passwd", "./x.bin"} {
		t.Run(path, func(t *testing.T) {
			_, err := runStream(t, svc, pushScript("data", "", path, []byte("x"), 0o644))
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// A volume is writable by the app that owns it, so a symlink pointing out of
// the volume is something an attacker can actually plant.
func TestSymlinkEscapeIsRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(volume, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := runStream(t, svc, pushScript("data", "", "escape/owned.bin", []byte("x"), 0o644))
	requireCode(t, err, codes.InvalidArgument)

	if _, statErr := os.Stat(filepath.Join(outside, "owned.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("file escaped the volume")
	}
}

// The prefix is client-supplied, so a symlink planted at it must not be able
// to redefine what "inside the volume" means for the rest of the session.
func TestSymlinkedPathPrefixIsRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(volume, "models")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := runStream(t, svc, pushScript("data", "models", "owned.bin", []byte("x"), 0o644))
	requireCode(t, err, codes.InvalidArgument)

	if _, statErr := os.Stat(filepath.Join(outside, "owned.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("file escaped the volume via the path prefix")
	}
}

// Organising artifacts into a subdirectory is the normal case, and the
// directory will not exist on the first push.
func TestPushCreatesMissingPrefixDirectories(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	if _, err := runStream(t, svc, pushScript("data", "models/fire", "v2.onnx", []byte("weights"), 0o644)); err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(volume, "models", "fire", "v2.onnx"))
	if err != nil {
		t.Fatalf("reading pushed file: %v", err)
	}
	if string(got) != "weights" {
		t.Fatalf("got %q, want weights", got)
	}
}

// A listing must not have side effects on the device.
func TestListingDoesNotCreatePrefixDirectory(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	if _, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     "data",
			PathPrefix: "models",
			Manifest:   &agentpbv2.FileSyncManifest{},
		}}},
	}); err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(volume, "models")); !os.IsNotExist(statErr) {
		t.Fatalf("listing created the prefix directory")
	}
}

func TestInvalidVolumeNamesAreRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	for _, volume := range []string{"..", ".", "../../etc", "sub/dir"} {
		t.Run(volume, func(t *testing.T) {
			_, err := runStream(t, svc, pushScript(volume, "", "x.bin", []byte("x"), 0o644))
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// Volumes exist because an app declared a persist entitlement. Creating one on
// demand would turn a typo into a push that reports success and lands where
// nothing reads it.
func TestUnknownVolumeIsNotFound(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	_, err := runStream(t, svc, pushScript("typo-data", "", "x.bin", []byte("x"), 0o644))
	requireCode(t, err, codes.NotFound)

	if _, statErr := os.Stat(filepath.Join(volumes, "typo-data")); !os.IsNotExist(statErr) {
		t.Fatalf("volume was created implicitly")
	}
}

func TestAppIDModeIsUnimplemented(t *testing.T) {
	svc, _ := newTestService(t)

	_, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			AppId:    "com.example.app",
			Manifest: &agentpbv2.FileSyncManifest{},
		}}},
	})
	requireCode(t, err, codes.Unimplemented)
}

// Filling the root partition breaks agent and OS updates, so a push that would
// do it fails before any bytes are written.
func TestPushIsRejectedWhenDiskIsFull(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	svc.availBytes = func(string) (int64, bool) { return fileSyncDiskReserve + 8, true }

	_, err := runStream(t, svc, pushScript("data", "", "model.bin", []byte("more than eight bytes"), 0o644))
	requireCode(t, err, codes.ResourceExhausted)

	entries, readErr := os.ReadDir(volume)
	if readErr != nil {
		t.Fatalf("reading volume: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no partial file, got %v", entries)
	}
}

// Free space that cannot be measured must not block every push.
func TestPushProceedsWhenFreeSpaceIsUnknown(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")
	svc.availBytes = func(string) (int64, bool) { return 0, false }

	if _, err := runStream(t, svc, pushScript("data", "", "model.bin", []byte("x"), 0o644)); err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
}

func TestChunkNotInManifestIsRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	msgs := pushScript("data", "", "declared.bin", []byte("x"), 0o644)
	msgs[1].GetChunk().Path = "undeclared.bin"

	_, err := runStream(t, svc, msgs)
	requireCode(t, err, codes.InvalidArgument)
}

func TestDeleteRemovesFile(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	target := filepath.Join(volume, "old.onnx")
	if err := os.WriteFile(target, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{},
		}}},
		{RequestType: &agentpbv2.FileSyncRequest_Delete{Delete: &agentpbv2.FileSyncDelete{
			Paths: []string{"old.onnx"},
		}}},
	})
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("file was not deleted")
	}
}

// Removing a whole subtree is what `volumes remove` is for; a file delete must
// not become a recursive one.
func TestDeleteRefusesDirectories(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	if err := os.MkdirAll(filepath.Join(volume, "models"), 0o755); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	_, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{},
		}}},
		{RequestType: &agentpbv2.FileSyncRequest_Delete{Delete: &agentpbv2.FileSyncDelete{
			Paths: []string{"models"},
		}}},
	})
	requireCode(t, err, codes.InvalidArgument)

	if _, statErr := os.Stat(filepath.Join(volume, "models")); statErr != nil {
		t.Fatalf("directory was removed: %v", statErr)
	}
}

// Replacing a model an app may be reading must be atomic: the app sees either
// the old bytes or the new ones.
func TestPushOverwritesExistingFile(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	target := filepath.Join(volume, "model.bin")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if _, err := runStream(t, svc, pushScript("data", "", "model.bin", []byte("v2-longer"), 0o644)); err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != "v2-longer" {
		t.Fatalf("got %q, want v2-longer", got)
	}
	entries, err := os.ReadDir(volume)
	if err != nil {
		t.Fatalf("reading volume: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// A transfer split across chunks is the normal case for a 50 MB artifact.
func TestMultiChunkTransfer(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")

	data := []byte("chunk-one-chunk-two-chunk-three")
	entry := entryFor("model.bin", data, 0o644)
	msgs := []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{Files: []*agentpbv2.FileSyncEntry{entry}},
		}}},
	}
	for i, seq := 0, uint64(0); i < len(data); i, seq = i+10, seq+1 {
		end := min(i+10, len(data))
		running := sha256.Sum256(data[:end])
		msgs = append(msgs, &agentpbv2.FileSyncRequest{
			RequestType: &agentpbv2.FileSyncRequest_Chunk{Chunk: &agentpbv2.FileSyncChunk{
				Path:           "model.bin",
				Data:           data[i:end],
				Sequence:       seq,
				CumulativeSize: int64(end),
				Sha256:         running[:],
			}},
		})
	}
	sum := sha256.Sum256(data)
	msgs = append(msgs, &agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Commit{Commit: &agentpbv2.FileSyncCommit{
			Path:   "model.bin",
			Sha256: sum[:],
			Size:   int64(len(data)),
		}},
	})

	if _, err := runStream(t, svc, msgs); err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(volume, "model.bin"))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("got %q, want %q", got, data)
	}
}

func TestOutOfOrderChunkIsRejected(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	msgs := pushScript("data", "", "model.bin", []byte("x"), 0o644)
	msgs[1].GetChunk().Sequence = 7

	_, err := runStream(t, svc, msgs)
	requireCode(t, err, codes.InvalidArgument)
}

func TestFirstMessageMustBeStart(t *testing.T) {
	svc, volumes := newTestService(t)
	makeVolume(t, volumes, "data")

	_, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Chunk{Chunk: &agentpbv2.FileSyncChunk{Path: "x"}}},
	})
	requireCode(t, err, codes.InvalidArgument)
}

// Temp files are an implementation detail of an in-flight push; advertising
// one would make a client diff against a file that does not exist yet.
func TestManifestSkipsTempFiles(t *testing.T) {
	svc, volumes := newTestService(t)
	volume := makeVolume(t, volumes, "data")
	if err := os.WriteFile(filepath.Join(volume, fileSyncTempPrefix+"abc~model.bin"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	stream, err := runStream(t, svc, []*agentpbv2.FileSyncRequest{
		{RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:   "data",
			Manifest: &agentpbv2.FileSyncManifest{},
		}}},
	})
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if files := stream.sent[0].GetManifest().GetFiles(); len(files) != 0 {
		t.Fatalf("temp file advertised: %+v", files)
	}
}
