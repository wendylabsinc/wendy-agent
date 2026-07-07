package services

import (
	"context"
	"io"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// WDY-1822 regression tests: a running task's output channel is its only
// drain. If the RPC handler that started the task stops consuming it (client
// disconnect, failed Send), back-pressure propagates through the agent's
// io.Pipes into the container's stdout FIFO and the app freezes in pipe_write
// once the buffers fill. These tests rebuild the containerd client's pipe
// chain (cio copier -> io.Pipe -> streamReader -> outputCh), fill it well past
// every buffer, and prove the app-side writes keep completing.

// disconnectedRunStream simulates a client that vanished before the Started
// response could be delivered: every Send fails.
type disconnectedRunStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *disconnectedRunStream) Send(*agentpb.RunContainerLayersResponse) error {
	return io.ErrClosedPipe
}

func (s *disconnectedRunStream) Context() context.Context { return s.ctx }

// pipeChainMock mirrors containerd.Client.StartContainer's IO wiring: the
// app's stdout is written into an io.Pipe (standing in for the cio copier
// reading the task FIFO) and a streamReader-style goroutine forwards it into
// a 64-slot output channel, exactly like streamOutput does.
type pipeChainMock struct {
	mockContainerdClient
	stdoutW *io.PipeWriter
}

func (m *pipeChainMock) StartContainer(_ context.Context, _ string, _ string, _ *agentpb.RestartPolicy) (<-chan ContainerOutput, error) {
	stdoutR, stdoutW := io.Pipe()
	m.stdoutW = stdoutW
	outputCh := make(chan ContainerOutput, 64)
	go func() {
		defer close(outputCh)
		buf := make([]byte, 32*1024)
		for {
			n, err := stdoutR.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				outputCh <- ContainerOutput{Stdout: data}
			}
			if err != nil {
				return
			}
		}
	}()
	return outputCh, nil
}

// writeAppStdout plays the container: it writes chunks x 32 KiB to stdout —
// far more than the pipe plus the 64-slot channel can absorb — and fails the
// test if any write blocks, which is exactly how the real app freezes.
func writeAppStdout(t *testing.T, w *io.PipeWriter, chunks int) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		chunk := make([]byte, 32*1024)
		for i := 0; i < chunks; i++ {
			if _, err := w.Write(chunk); err != nil {
				done <- err
				return
			}
		}
		done <- w.Close()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app stdout write failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("app stdout writes blocked: task output pipeline is not drained after client disconnect (WDY-1822)")
	}
}

func TestStreamContainerOutput_DrainsAfterImmediateClientDisconnect(t *testing.T) {
	mock := &pipeChainMock{}
	lm := NewContainerLogManager(zap.NewNop(), NewTelemetryBroadcaster())
	svc := NewContainerService(zap.NewNop(), mock, WithLogManager(lm))

	stream := &disconnectedRunStream{ctx: context.Background()}
	if err := svc.streamContainerOutput(context.Background(), "chatty-app", "", nil, stream); err == nil {
		t.Fatal("expected streamContainerOutput to fail when the client is gone")
	}

	// The detached app keeps logging long after the handler returned.
	writeAppStdout(t, mock.stdoutW, 256) // 8 MiB
}

func TestStreamContainerOutput_DrainsAfterDisconnectWithoutLogManager(t *testing.T) {
	mock := &pipeChainMock{}
	svc := NewContainerService(zap.NewNop(), mock) // no log manager configured

	stream := &disconnectedRunStream{ctx: context.Background()}
	if err := svc.streamContainerOutput(context.Background(), "chatty-app", "", nil, stream); err == nil {
		t.Fatal("expected streamContainerOutput to fail when the client is gone")
	}

	writeAppStdout(t, mock.stdoutW, 256) // 8 MiB
}
