//go:build unix

package services

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeSpawner records the first resize and command, echoes stdin to stdout, and
// returns exit code 5.
type fakeSpawner struct {
	mu        sync.Mutex
	gotCmd    []string
	gotResize [2]uint32
}

func (f *fakeSpawner) Run(_ context.Context, command []string, stdin io.Reader, stdout io.Writer, resize <-chan [2]uint32) (int, error) {
	f.mu.Lock()
	f.gotCmd = command
	f.mu.Unlock()
	if sz, ok := <-resize; ok {
		f.mu.Lock()
		f.gotResize = sz
		f.mu.Unlock()
	}
	go func() {
		for range resize {
		}
	}()
	buf := make([]byte, 1024)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			_, _ = stdout.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	return 5, nil
}

func startShellServer(t *testing.T, spawner HostShellSpawner) (agentpb.WendyShellServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	agentpb.RegisterWendyShellServiceServer(srv, NewShellService(zap.NewNop(), spawner))
	go func() { _ = srv.Serve(lis) }()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return agentpb.NewWendyShellServiceClient(conn), func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
}

func TestHostShell_RejectsMissingStart(t *testing.T) {
	client, cleanup := startShellServer(t, &fakeSpawner{})
	defer cleanup()

	stream, err := client.HostShell(context.Background())
	if err != nil {
		t.Fatalf("HostShell: %v", err)
	}
	// First frame is stdin, not Start.
	if err := stream.Send(&agentpb.HostShellRequest{RequestType: &agentpb.HostShellRequest_StdinData{StdinData: []byte("x")}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = stream.CloseSend()
	if _, rerr := stream.Recv(); status.Code(rerr) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", rerr)
	}
}

func TestHostShell_EmptyCommandAllowed_EchoesResizeExit(t *testing.T) {
	fs := &fakeSpawner{}
	client, cleanup := startShellServer(t, fs)
	defer cleanup()

	stream, err := client.HostShell(context.Background())
	if err != nil {
		t.Fatalf("HostShell: %v", err)
	}
	// Empty command => login shell; allowed (unlike container exec).
	if err := stream.Send(&agentpb.HostShellRequest{RequestType: &agentpb.HostShellRequest_Start_{
		Start: &agentpb.HostShellRequest_Start{TermSize: &agentpb.WindowSize{Rows: 30, Cols: 100}},
	}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := stream.Send(&agentpb.HostShellRequest{RequestType: &agentpb.HostShellRequest_StdinData{StdinData: []byte("ping")}}); err != nil {
		t.Fatalf("send stdin: %v", err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	var sawStdout bool
	var exit int32 = -1
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			t.Fatalf("recv: %v", rerr)
		}
		if len(resp.GetStdoutData()) > 0 {
			sawStdout = true
		}
		if _, ok := resp.GetResponseType().(*agentpb.HostShellResponse_ExitCode); ok {
			exit = resp.GetExitCode()
		}
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.gotCmd) != 0 {
		t.Errorf("command = %v; want empty (login shell)", fs.gotCmd)
	}
	if fs.gotResize != [2]uint32{30, 100} {
		t.Errorf("initial resize = %v; want [30 100]", fs.gotResize)
	}
	if !sawStdout {
		t.Error("stdout echo not streamed back")
	}
	if exit != 5 {
		t.Errorf("exit code = %d; want 5", exit)
	}
}
