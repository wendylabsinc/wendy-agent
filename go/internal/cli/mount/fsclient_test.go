package mount

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func newTestFSClient(t *testing.T) (*FSClient, string) {
	t.Helper()
	tmp := t.TempDir()
	services.SetVolumesDirForTest(tmp) // see Step 3 note
	t.Cleanup(services.ResetVolumesDirForTest)

	if err := os.MkdirAll(filepath.Join(tmp, "vol"), 0o755); err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyVolumeFsServiceServer(srv, services.NewVolumeFsService(zap.NewNop()))
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })
	return NewFSClient(context.Background(), agentpbv2.NewWendyVolumeFsServiceClient(conn), "vol"), tmp
}

func TestFSClientRoundTrip(t *testing.T) {
	c, root := newTestFSClient(t)
	if _, err := c.Create("f.txt", 0o644); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := c.WriteAt("f.txt", 0, []byte("payload")); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	data, eof, err := c.ReadAt("f.txt", 0, 100)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(data) != "payload" || !eof {
		t.Fatalf("got %q eof=%v", data, eof)
	}
	if _, err := os.Stat(filepath.Join(root, "vol", "f.txt")); err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
}
