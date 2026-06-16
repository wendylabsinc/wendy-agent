package services

import (
	"context"
	"crypto/sha256"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func startFileSyncTestServer(t *testing.T) (agentpb.WendyFileSyncServiceClient, func()) {
	t.Helper()
	root := t.TempDir()
	oldRoot := fileSyncRoot
	fileSyncRoot = root

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpb.RegisterWendyFileSyncServiceServer(srv, NewFileSyncService())
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}), grpc.WithInsecure())
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		fileSyncRoot = oldRoot
	}
	return agentpb.NewWendyFileSyncServiceClient(conn), cleanup
}

func TestFileSyncService_WritesCommittedFile(t *testing.T) {
	client, cleanup := startFileSyncTestServer(t)
	defer cleanup()

	data := []byte("hello\n")
	sum := sha256.Sum256(data)
	stream, err := client.SyncFiles(context.Background())
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if err := stream.Send(&agentpb.FileSyncRequest{RequestType: &agentpb.FileSyncRequest_Start{Start: &agentpb.FileSyncStart{
		AppId: "com.example.app",
		Manifest: &agentpb.FileSyncManifest{Files: []*agentpb.FileSyncEntry{{
			Path: "config/settings.txt", Size: int64(len(data)), Sha256: sum[:], Mode: 0o600,
		}}},
	}}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if resp, err := stream.Recv(); err != nil || resp.GetManifest() == nil {
		t.Fatalf("recv manifest = %v, %v", resp, err)
	}
	if err := stream.Send(&agentpb.FileSyncRequest{RequestType: &agentpb.FileSyncRequest_Chunk{Chunk: &agentpb.FileSyncChunk{
		Path: "config/settings.txt", Data: data, Sequence: 0, CumulativeSize: int64(len(data)), Sha256: sum[:],
	}}}); err != nil {
		t.Fatalf("send chunk: %v", err)
	}
	if err := stream.Send(&agentpb.FileSyncRequest{RequestType: &agentpb.FileSyncRequest_Commit{Commit: &agentpb.FileSyncCommit{
		Path: "config/settings.txt", Size: int64(len(data)), Sha256: sum[:],
	}}}); err != nil {
		t.Fatalf("send commit: %v", err)
	}
	if resp, err := stream.Recv(); err != nil || resp.GetAck().GetPath() != "config/settings.txt" {
		t.Fatalf("recv ack = %v, %v", resp, err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
	if resp, err := stream.Recv(); err != nil || resp.GetComplete() == nil {
		t.Fatalf("recv complete = %v, %v", resp, err)
	}

	appDir, err := FileSyncAppDir("com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(appDir, "config", "settings.txt"))
	if err != nil {
		t.Fatalf("read synced file: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q", got)
	}
	info, err := os.Stat(filepath.Join(appDir, "config", "settings.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestReceiveFileSyncChunk_RejectsPathMissingFromManifest(t *testing.T) {
	data := []byte("hello")
	sum := sha256.Sum256(data)
	err := receiveFileSyncChunk(t.TempDir(), map[string]*incomingFile{}, map[string]*agentpb.FileSyncEntry{}, &agentpb.FileSyncChunk{
		Path:           "missing.txt",
		Data:           data,
		Sequence:       0,
		CumulativeSize: int64(len(data)),
		Sha256:         sum[:],
	})
	if err == nil || !strings.Contains(err.Error(), "not present in manifest") {
		t.Fatalf("receiveFileSyncChunk error = %v, want missing manifest path", err)
	}
}

func TestReceiveFileSyncChunk_RejectsManifestSizeOverflow(t *testing.T) {
	data := []byte("hello")
	sum := sha256.Sum256(data)
	err := receiveFileSyncChunk(t.TempDir(), map[string]*incomingFile{}, map[string]*agentpb.FileSyncEntry{
		"payload.txt": {Path: "payload.txt", Size: int64(len(data) - 1), Sha256: sum[:], Mode: 0o644},
	}, &agentpb.FileSyncChunk{
		Path:           "payload.txt",
		Data:           data,
		Sequence:       0,
		CumulativeSize: int64(len(data)),
		Sha256:         sum[:],
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds manifest size") {
		t.Fatalf("receiveFileSyncChunk error = %v, want manifest size overflow", err)
	}
}

func TestReceiveFileSyncChunk_RejectsOversizedChunk(t *testing.T) {
	data := make([]byte, fileSyncMaxChunkSize+1)
	sum := sha256.Sum256(data)
	err := receiveFileSyncChunk(t.TempDir(), map[string]*incomingFile{}, map[string]*agentpb.FileSyncEntry{
		"payload.txt": {Path: "payload.txt", Size: int64(len(data)), Sha256: sum[:], Mode: 0o644},
	}, &agentpb.FileSyncChunk{
		Path:           "payload.txt",
		Data:           data,
		Sequence:       0,
		CumulativeSize: int64(len(data)),
		Sha256:         sum[:],
	})
	if err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("receiveFileSyncChunk error = %v, want oversized chunk", err)
	}
}

func TestFileSyncService_DeletesStaleFile(t *testing.T) {
	client, cleanup := startFileSyncTestServer(t)
	defer cleanup()

	appDir, err := FileSyncAppDir("com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(appDir, "old.txt")
	if err := os.WriteFile(stale, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	stream, err := client.SyncFiles(context.Background())
	if err != nil {
		t.Fatalf("SyncFiles: %v", err)
	}
	if err := stream.Send(&agentpb.FileSyncRequest{RequestType: &agentpb.FileSyncRequest_Start{Start: &agentpb.FileSyncStart{
		AppId: "com.example.app", Manifest: &agentpb.FileSyncManifest{},
	}}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv manifest: %v", err)
	}
	if len(resp.GetManifest().GetFiles()) != 1 || resp.GetManifest().GetFiles()[0].GetPath() != "old.txt" {
		t.Fatalf("manifest = %v", resp.GetManifest().GetFiles())
	}
	if err := stream.Send(&agentpb.FileSyncRequest{RequestType: &agentpb.FileSyncRequest_Delete{Delete: &agentpb.FileSyncDelete{Paths: []string{"old.txt"}}}}); err != nil {
		t.Fatalf("send delete: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("close send: %v", err)
	}
	if resp, err := stream.Recv(); err != nil && err != io.EOF || err == nil && resp.GetComplete() == nil {
		t.Fatalf("recv complete = %v, %v", resp, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file still exists or stat failed: %v", err)
	}
}
