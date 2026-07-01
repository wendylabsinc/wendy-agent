package localsocket

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestListen_CreatesSocketAndServesPlainGRPC(t *testing.T) {
	// Build the socket path under a short temp dir: t.TempDir() can exceed the
	// unix-socket sun_path limit (~104 bytes on macOS), making bind fail with
	// "invalid argument". The nested "n" dir keeps the parent-dir coverage.
	dir, err := os.MkdirTemp("/tmp", "ls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "n", "agent.sock")

	lis, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer lis.Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode = %o, want 660", perm)
	}

	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check over UDS failed (plain gRPC, no mTLS): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

func TestListen_RemovesStaleSocket(t *testing.T) {
	// Short temp dir to stay under the unix-socket sun_path limit (see above).
	dir, err := os.MkdirTemp("/tmp", "ls")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	lis, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	lis.Close()
}
