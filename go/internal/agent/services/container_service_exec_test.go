package services

import (
	"context"
	"io"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// execTestMock embeds the full ContainerdClient mock and adds ExecInContainer so
// it also satisfies ContainerExecer. It echoes stdin to stdout until EOF, records
// the start args + first resize, then returns exit code 7.
type execTestMock struct {
	*mockContainerdClient

	mu        sync.Mutex
	gotApp    string
	gotCmd    []string
	gotTTY    bool
	gotResize [2]uint32
}

func (m *execTestMock) ExecInContainer(_ context.Context, appName string, command []string, tty bool, stdin io.Reader, stdout, _ io.Writer, resize <-chan [2]uint32) (int, error) {
	m.mu.Lock()
	m.gotApp, m.gotCmd, m.gotTTY = appName, command, tty
	m.mu.Unlock()

	if sz, ok := <-resize; ok { // initial term_size pushed by the handler
		m.mu.Lock()
		m.gotResize = sz
		m.mu.Unlock()
	}
	// Drain any remaining resizes in the background so the channel never blocks.
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
	return 7, nil
}

func TestExecContainer_RejectsEmptyCommand(t *testing.T) {
	// Exec over the admin socket runs arbitrary commands in any container, so the
	// handler must not fall back to a default shell — an ExecStart with no command
	// is rejected before any exec is attempted.
	mock := &execTestMock{mockContainerdClient: &mockContainerdClient{}}
	client, cleanup := startContainerServer(t, mock)
	defer cleanup()

	stream, err := client.ExecContainer(context.Background())
	if err != nil {
		t.Fatalf("ExecContainer: %v", err)
	}
	if err := stream.Send(&agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Start{
		Start: &agentpb.ExecContainerRequest_ExecStart{AppName: "myapp"}, // no Command
	}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	_ = stream.CloseSend()

	if _, rerr := stream.Recv(); status.Code(rerr) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for empty command, got %v", rerr)
	}
}

func TestExecContainer_EchoesStdinAppliesResizeAndReturnsExit(t *testing.T) {
	mock := &execTestMock{mockContainerdClient: &mockContainerdClient{}}
	client, cleanup := startContainerServer(t, mock)
	defer cleanup()

	stream, err := client.ExecContainer(context.Background())
	if err != nil {
		t.Fatalf("ExecContainer: %v", err)
	}

	if err := stream.Send(&agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Start{
		Start: &agentpb.ExecContainerRequest_ExecStart{
			AppName:  "myapp",
			Command:  []string{"claude"},
			Tty:      true,
			TermSize: &agentpb.WindowSize{Rows: 40, Cols: 120},
		},
	}}); err != nil {
		t.Fatalf("send start: %v", err)
	}
	if err := stream.Send(&agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_StdinData{StdinData: []byte("hi")}}); err != nil {
		t.Fatalf("send stdin: %v", err)
	}
	// Close send so the server's stdin reader reaches EOF and the mock returns.
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
		if _, ok := resp.GetResponseType().(*agentpb.ExecContainerResponse_ExitCode); ok {
			exit = resp.GetExitCode()
		}
	}

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if mock.gotApp != "myapp" {
		t.Errorf("app = %q; want myapp", mock.gotApp)
	}
	if len(mock.gotCmd) != 1 || mock.gotCmd[0] != "claude" {
		t.Errorf("command = %v; want [claude]", mock.gotCmd)
	}
	if !mock.gotTTY {
		t.Error("tty not forwarded")
	}
	if mock.gotResize != [2]uint32{40, 120} {
		t.Errorf("initial resize = %v; want [40 120]", mock.gotResize)
	}
	if !sawStdout {
		t.Error("stdout echo not streamed back")
	}
	if exit != 7 {
		t.Errorf("exit code = %d; want 7", exit)
	}
}
