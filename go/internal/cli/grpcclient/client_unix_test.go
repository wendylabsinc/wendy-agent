package grpcclient

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
)

// fakeAgentServer implements just GetAgentVersion for the smoke assertion.
type fakeAgentServer struct {
	agentpb.UnimplementedWendyAgentServiceServer
}

func (fakeAgentServer) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{Version: "test"}, nil
}

func TestConnectUnix_DialsUDSWithoutTLS(t *testing.T) {
	// Build the socket path under a short temp dir: t.TempDir() can exceed the
	// unix-socket sun_path limit (~104 bytes on macOS), making bind fail with
	// "invalid argument" (matches the convention in localsocket_test.go).
	dir, err := os.MkdirTemp("/tmp", "wsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")

	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}

	srv := grpc.NewServer()
	agentpb.RegisterWendyAgentServiceServer(srv, fakeAgentServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := ConnectUnix(ctx, sock)
	if err != nil {
		t.Fatalf("ConnectUnix: %v", err)
	}
	defer conn.Close()

	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		t.Fatalf("GetAgentVersion over UDS: %v", err)
	}
	if resp.GetVersion() != "test" {
		t.Fatalf("got version %q, want %q", resp.GetVersion(), "test")
	}
}
