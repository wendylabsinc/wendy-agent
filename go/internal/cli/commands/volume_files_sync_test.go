//go:build !windows

// These tests drive the CLI's file sync client against the real agent service,
// which is the only way to catch the two sides disagreeing about the protocol.
// That import reaches Linux-oriented agent code (audio/pipewire) that cannot
// cross-compile for Windows, and CI type-checks this package for Windows —
// hence the build tag. The Windows CLI cannot host an agent anyway.

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// newVolumeSyncClient wires the CLI's file sync client to the real agent
// service over an in-memory connection, so these tests exercise the actual
// protocol both sides speak rather than a mock's idea of it.
func newVolumeSyncClient(t *testing.T) (agentpbv2.WendyFileSyncServiceClient, string) {
	t.Helper()

	volumesDir := t.TempDir()
	svc := services.NewFileSyncServiceV2(zap.NewNop(), services.WithVolumesDir(volumesDir))

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyFileSyncServiceServer(srv, svc)
	go func() {
		if err := srv.Serve(lis); err != nil {
			return
		}
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		srv.Stop()
	})

	return agentpbv2.NewWendyFileSyncServiceClient(conn), volumesDir
}

func makeVolumeDir(t *testing.T, volumesDir, name string) string {
	t.Helper()
	dir := filepath.Join(volumesDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating volume: %v", err)
	}
	return dir
}

func writeLocalFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("writing local file: %v", err)
	}
	return p
}

func TestVolumePushLandsFileOnDevice(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")
	local := writeLocalFile(t, "sffa_yolo.onnx", []byte("model weights"))

	err := runVolumePush(context.Background(), client, local,
		volumeSpec{volume: "app-data", path: "models/sffa_yolo.onnx"})
	if err != nil {
		t.Fatalf("runVolumePush: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(volume, "models", "sffa_yolo.onnx"))
	if err != nil {
		t.Fatalf("reading pushed file: %v", err)
	}
	if string(got) != "model weights" {
		t.Fatalf("got %q, want %q", got, "model weights")
	}
}

// A file larger than one chunk is the case that matters: a 50 MB model is
// hundreds of chunks, and the running digest must line up on both sides.
func TestVolumePushMultiChunkFile(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")

	data := make([]byte, volumeChunkSize*2+1024)
	for i := range data {
		data[i] = byte(i % 251)
	}
	local := writeLocalFile(t, "big.bin", data)

	if err := runVolumePush(context.Background(), client, local,
		volumeSpec{volume: "app-data", path: "big.bin"}); err != nil {
		t.Fatalf("runVolumePush: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(volume, "big.bin"))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatalf("content digest mismatch after push")
	}
}

func TestVolumePushDirectory(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")

	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "nested", "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := runVolumePush(context.Background(), client, localDir,
		volumeSpec{volume: "app-data", path: "calibration"}); err != nil {
		t.Fatalf("runVolumePush: %v", err)
	}

	for rel, want := range map[string]string{
		"calibration/a.txt":        "a",
		"calibration/nested/b.txt": "b",
	} {
		got, err := os.ReadFile(filepath.Join(volume, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s: got %q, want %q", rel, got, want)
		}
	}
}

// Re-pushing an unchanged file must not resend it — the whole point of the
// manifest exchange on a slow site link.
func TestVolumePushSkipsUnchangedFile(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")
	local := writeLocalFile(t, "model.bin", []byte("same bytes"))

	spec := volumeSpec{volume: "app-data", path: "model.bin"}
	if err := runVolumePush(context.Background(), client, local, spec); err != nil {
		t.Fatalf("first push: %v", err)
	}
	target := filepath.Join(volume, "model.bin")
	first, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := runVolumePush(context.Background(), client, local, spec); err != nil {
		t.Fatalf("second push: %v", err)
	}
	second, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// An unchanged file is left alone entirely, so its mtime does not move.
	if !first.ModTime().Equal(second.ModTime()) {
		t.Fatalf("unchanged file was rewritten (mtime %v -> %v)", first.ModTime(), second.ModTime())
	}
}

// Swapping a model is the core workflow: the new bytes must replace the old
// ones with no temp files left in the volume the app reads.
func TestVolumePushReplacesExistingFile(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")
	if err := os.WriteFile(filepath.Join(volume, "model.bin"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	local := writeLocalFile(t, "model.bin", []byte("v2 weights"))

	if err := runVolumePush(context.Background(), client, local,
		volumeSpec{volume: "app-data", path: "model.bin"}); err != nil {
		t.Fatalf("runVolumePush: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(volume, "model.bin"))
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if string(got) != "v2 weights" {
		t.Fatalf("got %q, want v2 weights", got)
	}
	entries, err := os.ReadDir(volume)
	if err != nil {
		t.Fatalf("reading volume: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the model, got %d entries", len(entries))
	}
}

func TestVolumeListReportsDeviceDigest(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")
	data := []byte("model weights")
	if err := os.MkdirAll(filepath.Join(volume, "models"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(volume, "models", "a.onnx"), data, 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	stream, err := client.SyncFiles(context.Background())
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if err := stream.Send(&agentpbv2.FileSyncRequest{
		RequestType: &agentpbv2.FileSyncRequest_Start{Start: &agentpbv2.FileSyncStart{
			Volume:     "app-data",
			PathPrefix: "models",
			Manifest:   &agentpbv2.FileSyncManifest{},
		}},
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	remote, err := recvManifest(stream)
	if err != nil {
		t.Fatalf("recvManifest: %v", err)
	}

	entry, ok := remote["a.onnx"]
	if !ok {
		t.Fatalf("expected a.onnx in %v", remote)
	}
	want := sha256.Sum256(data)
	if hex.EncodeToString(entry.GetSha256()) != hex.EncodeToString(want[:]) {
		t.Fatalf("digest mismatch: got %x, want %x", entry.GetSha256(), want)
	}
}

func TestVolumeRemoveFileDeletesOnDevice(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	volume := makeVolumeDir(t, volumesDir, "app-data")
	target := filepath.Join(volume, "old.onnx")
	if err := os.WriteFile(target, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	if err := runVolumeRemoveFile(context.Background(), client,
		volumeSpec{volume: "app-data", path: "old.onnx"}); err != nil {
		t.Fatalf("runVolumeRemoveFile: %v", err)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

// The device's rejection has to reach the user as an error, not a silent
// success — a push that reports OK but wrote nothing is the failure mode this
// whole feature exists to remove.
func TestVolumePushSurfacesUnknownVolume(t *testing.T) {
	client, volumesDir := newVolumeSyncClient(t)
	makeVolumeDir(t, volumesDir, "app-data")
	local := writeLocalFile(t, "model.bin", []byte("x"))

	err := runVolumePush(context.Background(), client, local,
		volumeSpec{volume: "typo-data", path: "model.bin"})
	if err == nil {
		t.Fatal("expected an error for an unknown volume")
	}
	if !strings.Contains(status.Convert(err).Message(), "does not exist") {
		t.Fatalf("unhelpful error: %v", err)
	}
}
