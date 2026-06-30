package services

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/protobuf/proto"
)

func startVolumeFsServer(t *testing.T) (agentpbv2.WendyVolumeFsServiceClient, string) {
	t.Helper()
	tmp := t.TempDir()
	old := volumesDir
	volumesDir = tmp
	t.Cleanup(func() { volumesDir = old })

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpbv2.RegisterWendyVolumeFsServiceServer(srv, NewVolumeFsService(zap.NewNop()))
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })
	return agentpbv2.NewWendyVolumeFsServiceClient(conn), tmp
}

func TestVolumeFsStatAndReadDir(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(filepath.Join(vdir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := cl.Stat(context.Background(), &agentpbv2.StatRequest{Volume: "vol", Path: "a.txt"})
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.GetAttr().GetType() != agentpbv2.FileType_FILE_TYPE_REGULAR || st.GetAttr().GetSize() != 5 {
		t.Fatalf("unexpected attr: %+v", st.GetAttr())
	}

	rd, err := cl.ReadDir(context.Background(), &agentpbv2.ReadDirRequest{Volume: "vol", Path: ""})
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(rd.GetEntries()) != 2 {
		t.Fatalf("want 2 entries, got %d", len(rd.GetEntries()))
	}
}

func TestVolumeFsRead(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vdir, "a.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := cl.Read(context.Background(), &agentpbv2.ReadRequest{Volume: "vol", Path: "a.txt", Offset: 6, Length: 100})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(resp.GetData()) != "world" || !resp.GetEof() {
		t.Fatalf("got %q eof=%v", resp.GetData(), resp.GetEof())
	}
}

func TestVolumeFsStatFs(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	if err := os.MkdirAll(filepath.Join(root, "vol"), 0o755); err != nil {
		t.Fatal(err)
	}
	resp, err := cl.StatFs(context.Background(), &agentpbv2.StatFsRequest{Volume: "vol"})
	if err != nil {
		t.Fatalf("StatFs: %v", err)
	}
	if resp.GetTotalBytes() == 0 || resp.GetFreeBytes() == 0 {
		t.Fatalf("expected non-zero total/free, got total=%d free=%d", resp.GetTotalBytes(), resp.GetFreeBytes())
	}
}

func TestVolumeFsWriteCreateRename(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	vdir := filepath.Join(root, "vol")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := cl.Create(ctx, &agentpbv2.CreateRequest{Volume: "vol", Path: "new.txt", Mode: 0o644}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := cl.Write(ctx, &agentpbv2.WriteRequest{Volume: "vol", Path: "new.txt", Offset: 0, Data: []byte("abcdef")}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := cl.SetAttr(ctx, &agentpbv2.SetAttrRequest{Volume: "vol", Path: "new.txt", Size: proto.Int64(3)}); err != nil {
		t.Fatalf("SetAttr truncate: %v", err)
	}
	if _, err := cl.Rename(ctx, &agentpbv2.RenameRequest{Volume: "vol", From: "new.txt", To: "renamed.txt"}); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(vdir, "renamed.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "abc" {
		t.Fatalf("want %q got %q", "abc", got)
	}

	if _, err := cl.Mkdir(ctx, &agentpbv2.MkdirRequest{Volume: "vol", Path: "d", Mode: 0o755}); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := cl.Rmdir(ctx, &agentpbv2.RmdirRequest{Volume: "vol", Path: "d"}); err != nil {
		t.Fatalf("Rmdir: %v", err)
	}
	if _, err := cl.Unlink(ctx, &agentpbv2.UnlinkRequest{Volume: "vol", Path: "renamed.txt"}); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
}

func TestVolumeFsRejectsTraversal(t *testing.T) {
	cl, root := startVolumeFsServer(t)
	if err := os.MkdirAll(filepath.Join(root, "vol"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := cl.Stat(context.Background(), &agentpbv2.StatRequest{Volume: "vol", Path: "../../etc/passwd"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}
