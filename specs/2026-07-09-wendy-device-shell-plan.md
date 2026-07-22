# `wendy device shell` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wendy device shell` — an interactive host TTY (login shell / bash) on a WendyOS device over the existing mTLS/PKI gRPC trust.

**Architecture:** A new `WendyShellService.HostShell` bidirectional-streaming RPC mirrors the existing `WendyContainerService.ExecContainer` wire shape (Start / stdin / resize → stdout / exit_code). A new agent-side `hostexec` package spawns the host PTY via `github.com/creack/pty`; a `ShellService` gRPC handler wires the stream to it. The CLI command is a near-copy of `device attach`, reusing `connectToAgent`, raw-mode, and the SIGWINCH helper.

**Tech Stack:** Go, gRPC (protoc + protoc-gen-go/-go-grpc via `scripts/generate-proto.sh`), `github.com/creack/pty`, `golang.org/x/term`, cobra, zap.

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy`; `go.mod` is at the **repo root**. All build/test/proto commands run from the `go/` directory (that's how the Makefile invokes them; relative package paths like `./cmd/wendy` resolve against cwd).
- Design spec: `specs/2026-07-09-wendy-device-shell-design.md`. Follow it exactly.
- **Access gate: PKI trust only.** The service is registered on all servers unconditionally; do NOT add a debug/opt-in flag. Log one structured audit line at session start and end.
- **Scope (v1):** root only (no `--user`); TTY-only (CLI errors if stdin is not an interactive terminal); PTY master merges stdout+stderr → all output on `stdout_data`.
- Reuse the existing `WindowSize` message (defined in `wendy_agent_v1_container_service.proto`) — do NOT declare a second `WindowSize` (same Go package `agentpb`, would collide).
- Swift proto is intentionally NOT regenerated (this is a Go-only CLI+agent feature).
- The `hostexec` package and `shell_service.go` are Unix-only (`//go:build unix`) — `creack/pty` has no Windows support, and the agent runs only on Linux. This keeps `go build ./...` / tests working on the developer's macOS.

---

### Task 1: Proto — `WendyShellService` + code generation

**Files:**
- Create: `Proto/wendy/agent/services/v1/wendy_agent_v1_shell_service.proto`
- Modify: `go/scripts/generate-proto.sh` (add the new proto to the `AGENT_PROTOS` array)
- Generated (by the script): `go/proto/gen/agentpb/wendy_agent_v1_shell_service.pb.go`, `go/proto/gen/agentpb/wendy_agent_v1_shell_service_grpc.pb.go`

**Interfaces:**
- Produces: Go types in package `agentpb` — `WendyShellServiceServer`/`Client`, `RegisterWendyShellServiceServer`, `NewWendyShellServiceClient`, `HostShellRequest` (with `_Start`, `_StdinData`, `_Resize` oneof wrappers and nested `HostShellRequest_Start`), `HostShellResponse` (with `_StdoutData`, `_ExitCode`). Reuses `agentpb.WindowSize`.

- [ ] **Step 1: Create the proto file**

Create `Proto/wendy/agent/services/v1/wendy_agent_v1_shell_service.proto`:

```proto
syntax = "proto3";

package wendy.agent.services.v1;

// WindowSize is defined in the container service proto. Both files generate into
// the same Go package (agentpb), so importing it reuses the type rather than
// declaring a colliding duplicate.
import "wendy/agent/services/v1/wendy_agent_v1_container_service.proto";

// WendyShellService opens an interactive shell on the device *host* (not inside a
// container). It is the host-level analogue of WendyContainerService.ExecContainer.
service WendyShellService {
    // HostShell opens an interactive host PTY. The first client message MUST be
    // Start; subsequent messages carry stdin or window resizes. The server streams
    // stdout (the PTY master merges stderr in) and a final exit_code.
    rpc HostShell(stream HostShellRequest) returns (stream HostShellResponse);
}

message HostShellRequest {
    message Start {
        // Empty => resolve and run the target user's login shell.
        // Non-empty => run this argv with a TTY.
        repeated string command = 1;
        WindowSize term_size = 2;
    }
    oneof request_type {
        Start start = 1;        // first frame, required
        bytes stdin_data = 2;   // subsequent stdin
        WindowSize resize = 3;  // subsequent resize events
    }
}

message HostShellResponse {
    oneof response_type {
        bytes stdout_data = 1;  // PTY master output (stdout+stderr merged)
        int32 exit_code = 2;    // final frame
    }
}
```

- [ ] **Step 2: Register the proto with the generator**

In `go/scripts/generate-proto.sh`, add the new file to the `AGENT_PROTOS=( ... )` array (right after the container service entry):

```bash
    "wendy/agent/services/v1/wendy_agent_v1_container_service.proto"
    "wendy/agent/services/v1/wendy_agent_v1_shell_service.proto"
```

- [ ] **Step 3: Generate code**

Run: `cd go && make proto`
Expected: exits 0; `git status` shows new files `go/proto/gen/agentpb/wendy_agent_v1_shell_service.pb.go` and `..._grpc.pb.go` (plus possibly regenerated sibling files — that's fine, the script cleans and regenerates the whole gen dir).

- [ ] **Step 4: Verify the generated symbols compile and exist**

Run: `cd go && go build ./proto/... && grep -l "RegisterWendyShellServiceServer" proto/gen/agentpb/*.go`
Expected: build succeeds; grep prints the grpc file path.

- [ ] **Step 5: Commit**

```bash
git add Proto/wendy/agent/services/v1/wendy_agent_v1_shell_service.proto go/scripts/generate-proto.sh go/proto/gen/agentpb/
git commit -m "feat(proto): add WendyShellService.HostShell RPC"
```

---

### Task 2: Host PTY spawner — `hostexec` package

**Files:**
- Create: `go/internal/agent/hostexec/hostexec.go`
- Test: `go/internal/agent/hostexec/hostexec_test.go`
- Modify: `go.mod` / `go.sum` (add `github.com/creack/pty`)

**Interfaces:**
- Produces:
  - `func New() *Spawner`
  - `func (Spawner) Run(ctx context.Context, command []string, stdin io.Reader, stdout io.Writer, resize <-chan [2]uint32) (int, error)` — starts `command` (or root's login shell when `command` is empty) on a PTY, copies stdin↔stdout, applies resizes (`[rows, cols]`), returns the child exit code.
  - unexported `shellFromPasswd(path, username string) string` (tested).

- [ ] **Step 1: Add the PTY dependency**

Run: `cd go && go get github.com/creack/pty && go mod tidy`
Expected: `github.com/creack/pty` appears in `go.mod`.

- [ ] **Step 2: Write the failing tests**

Create `go/internal/agent/hostexec/hostexec_test.go`:

```go
//go:build unix

package hostexec

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellFromPasswd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "passwd")
	content := "daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n" +
		"root:x:0:0:root:/root:/bin/bash\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := shellFromPasswd(p, "root"); got != "/bin/bash" {
		t.Fatalf("shellFromPasswd = %q; want /bin/bash", got)
	}
	if got := shellFromPasswd(p, "missing"); got != "" {
		t.Fatalf("shellFromPasswd(missing) = %q; want empty", got)
	}
}

func TestRun_EchoesAndReturnsExit(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	var out strings.Builder
	resize := make(chan [2]uint32, 1)
	resize <- [2]uint32{24, 80}

	done := make(chan int, 1)
	go func() {
		code, err := New().Run(context.Background(),
			[]string{"/bin/sh", "-c", "printf hello; exit 3"},
			stdinR, &out, resize)
		if err != nil {
			t.Errorf("Run: %v", err)
		}
		done <- code
	}()

	// The command ignores stdin and exits on its own; close stdin so the
	// spawner's stdin copy goroutine also unwinds, then close resize.
	_ = stdinW.Close()
	close(resize)

	select {
	case code := <-done:
		if code != 3 {
			t.Fatalf("exit code = %d; want 3", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return in time")
	}
	if !strings.Contains(out.String(), "hello") {
		t.Fatalf("stdout = %q; want to contain hello", out.String())
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/hostexec/...`
Expected: FAIL — package/function does not exist (`undefined: New`, `undefined: shellFromPasswd`).

- [ ] **Step 4: Write the implementation**

Create `go/internal/agent/hostexec/hostexec.go`:

```go
//go:build unix

// Package hostexec runs an interactive login shell (or an explicit command) on
// the device host with a PTY. It is the host-level analogue of container exec:
// containerd owns the PTY for in-container exec, but a host shell needs its own
// os/exec + pty path.
package hostexec

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

// Spawner runs a host PTY session. It holds no state; New returns a ready value.
type Spawner struct{}

// New returns a host shell spawner.
func New() *Spawner { return &Spawner{} }

// Run starts command (or root's login shell when command is empty) attached to a
// newly allocated PTY, copies stdin into the PTY and PTY output into stdout,
// applies window resizes ([rows, cols]) until the resize channel closes, and
// returns the child's exit code. stderr is merged into the PTY master, so all
// output arrives via stdout.
func (Spawner) Run(ctx context.Context, command []string, stdin io.Reader, stdout io.Writer, resize <-chan [2]uint32) (int, error) {
	argv := command
	if len(argv) == 0 {
		argv = []string{loginShell()}
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "HOME=/root", "TERM=xterm-256color")
	cmd.Dir = "/root"

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return 0, fmt.Errorf("starting host pty for %q: %w", argv[0], err)
	}
	defer func() { _ = ptmx.Close() }()

	// Apply resizes until the channel closes.
	go func() {
		for sz := range resize {
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(sz[0]), Cols: uint16(sz[1])})
		}
	}()

	// stdin -> PTY master. Returns when the handler closes the stdin reader.
	go func() { _, _ = io.Copy(ptmx, stdin) }()

	// PTY master -> stdout. Returns when the child exits and the master EOFs.
	_, _ = io.Copy(stdout, ptmx)

	return exitCode(cmd.Wait()), nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// loginShell resolves root's login shell from /etc/passwd, falling back to $SHELL
// and then /bin/sh.
func loginShell() string {
	if sh := shellFromPasswd("/etc/passwd", "root"); sh != "" {
		return sh
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// shellFromPasswd returns the login shell (7th field) for username in a
// passwd-format file, or "" if the file is unreadable or the user is absent.
func shellFromPasswd(path, username string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) >= 7 && fields[0] == username {
			return fields[6]
		}
	}
	return ""
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/hostexec/...`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/hostexec/ go.mod go.sum
git commit -m "feat(agent): host PTY spawner (hostexec) with login-shell resolution"
```

---

### Task 3: Agent gRPC handler — `ShellService`

**Files:**
- Create: `go/internal/agent/services/shell_service.go`
- Test: `go/internal/agent/services/shell_service_test.go`

**Interfaces:**
- Consumes: `agentpb.WendyShellServiceServer` and message types (Task 1); the `Run(...)` signature (Task 2).
- Produces:
  - `type HostShellSpawner interface { Run(ctx context.Context, command []string, stdin io.Reader, stdout io.Writer, resize <-chan [2]uint32) (int, error) }`
  - `func NewShellService(logger *zap.Logger, spawner HostShellSpawner) *ShellService`
  - `ShellService.HostShell(stream grpc.BidiStreamingServer[agentpb.HostShellRequest, agentpb.HostShellResponse]) error`

- [ ] **Step 1: Write the failing tests**

The generated oneof wrapper for the nested `Start` message is `agentpb.HostShellRequest_Start_` (note the trailing underscore); it holds a `Start *agentpb.HostShellRequest_Start`. Create `go/internal/agent/services/shell_service_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/services/ -run TestHostShell`
Expected: FAIL — `undefined: NewShellService` / `undefined: HostShellSpawner`.

- [ ] **Step 3: Write the implementation**

Create `go/internal/agent/services/shell_service.go`:

```go
//go:build unix

package services

import (
	"context"
	"io"
	"sync"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// HostShellSpawner runs an interactive host PTY. It is injected so the service
// can be tested without spawning a real shell (see hostexec.Spawner).
type HostShellSpawner interface {
	Run(ctx context.Context, command []string, stdin io.Reader, stdout io.Writer, resize <-chan [2]uint32) (int, error)
}

// ShellService serves WendyShellService: an interactive shell on the device host.
type ShellService struct {
	agentpb.UnimplementedWendyShellServiceServer
	logger  *zap.Logger
	spawner HostShellSpawner
}

// NewShellService constructs the host shell service.
func NewShellService(logger *zap.Logger, spawner HostShellSpawner) *ShellService {
	return &ShellService{logger: logger, spawner: spawner}
}

// HostShell opens an interactive host PTY. The first client message must be
// Start; later messages carry stdin or window resizes. Output streams back as
// stdout frames, followed by a final exit_code.
func (s *ShellService) HostShell(stream grpc.BidiStreamingServer[agentpb.HostShellRequest, agentpb.HostShellResponse]) error {
	first, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "missing first shell message")
	}
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first message must be Start")
	}
	command := start.GetCommand()

	// Audit: a host shell over mTLS is a root shell on the device. Log every
	// session and its outcome so there is a forensic trail. An empty command
	// means the resolved login shell.
	s.logger.Info("host shell started", zap.Strings("command", command))
	startedAt := time.Now()

	ctx := stream.Context()
	stdinR, stdinW := io.Pipe()
	defer stdinR.Close()

	resize := make(chan [2]uint32, 8)
	if ts := start.GetTermSize(); ts != nil {
		resize <- [2]uint32{ts.GetRows(), ts.GetCols()}
	}

	// Forward stdin + resize frames until the client closes the stream.
	go func() {
		defer stdinW.Close()
		defer close(resize)
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				return
			}
			switch {
			case len(msg.GetStdinData()) > 0:
				if _, werr := stdinW.Write(msg.GetStdinData()); werr != nil {
					return
				}
			case msg.GetResize() != nil:
				select {
				case resize <- [2]uint32{msg.GetResize().GetRows(), msg.GetResize().GetCols()}:
				default: // drop resizes if the consumer is momentarily behind
				}
			}
		}
	}()

	// gRPC forbids concurrent SendMsg; serialize the stdout writer and the final
	// exit_code frame through one lock.
	sender := &shellSender{stream: stream}
	stdout := &shellWriter{sender: sender}

	code, err := s.spawner.Run(ctx, command, stdinR, stdout, resize)
	if err != nil {
		s.logger.Warn("host shell failed",
			zap.Duration("duration", time.Since(startedAt)), zap.Error(err))
		return status.Errorf(codes.Internal, "host shell failed: %v", err)
	}
	s.logger.Info("host shell completed",
		zap.Int("exit_code", code), zap.Duration("duration", time.Since(startedAt)))
	return sender.send(&agentpb.HostShellResponse{
		ResponseType: &agentpb.HostShellResponse_ExitCode{ExitCode: int32(code)},
	})
}

// shellSender serializes Send on the host shell bidi stream.
type shellSender struct {
	stream grpc.BidiStreamingServer[agentpb.HostShellRequest, agentpb.HostShellResponse]
	mu     sync.Mutex
}

func (s *shellSender) send(resp *agentpb.HostShellResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(resp)
}

// shellWriter adapts the PTY output stream to io.Writer, forwarding it as
// stdout_data frames.
type shellWriter struct{ sender *shellSender }

func (w *shellWriter) Write(p []byte) (int, error) {
	// Copy: the PTY may reuse its read buffer once Write returns.
	buf := append([]byte(nil), p...)
	if err := w.sender.send(&agentpb.HostShellResponse{
		ResponseType: &agentpb.HostShellResponse_StdoutData{StdoutData: buf},
	}); err != nil {
		return 0, err
	}
	return len(p), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run TestHostShell`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/shell_service.go go/internal/agent/services/shell_service_test.go
git commit -m "feat(agent): WendyShellService.HostShell handler over host PTY"
```

---

### Task 4: Register `ShellService` in the agent

**Files:**
- Modify: `go/cmd/wendy-agent/main.go` (construct the service ~line 231 near `containerSvc`; register it in `registerAllServices` ~line 460)

**Interfaces:**
- Consumes: `services.NewShellService`, `hostexec.New` (Tasks 2–3); `agentpb.RegisterWendyShellServiceServer` (Task 1).

- [ ] **Step 1: Add the import**

In `go/cmd/wendy-agent/main.go`, add to the import block:

```go
	"github.com/wendylabsinc/wendy/go/internal/agent/hostexec"
```

(The file already imports `services` and `agentpb`.)

- [ ] **Step 2: Construct the service**

Immediately after the `containerSvc := services.NewContainerService(...)` block (ends ~line 233), add:

```go
	shellSvc := services.NewShellService(logger, hostexec.New())
```

- [ ] **Step 3: Register on all servers**

In `registerAllServices`, right after the `agentpb.RegisterWendyContainerServiceServer(srv, containerSvc)` line (~461), add:

```go
	agentpb.RegisterWendyShellServiceServer(srv, shellSvc)
```

- [ ] **Step 4: Build the agent**

Run: `cd go && make build-agent`
Expected: exits 0, produces `go/bin/wendy-agent`.

- [ ] **Step 5: Commit**

```bash
git add go/cmd/wendy-agent/main.go
git commit -m "feat(agent): register WendyShellService on all servers"
```

---

### Task 5: CLI — `wendy device shell`

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (add `ShellService` field to `AgentConnection`; construct in `newAgentConnection`)
- Create: `go/internal/cli/commands/device_shell.go`
- Modify: `go/internal/cli/commands/device.go` (register `newDeviceShellCmd()` in the `manage` group)

**Interfaces:**
- Consumes: `agentpb.NewWendyShellServiceClient`, `HostShellRequest`/`Response` (Task 1); `connectToAgent`, `SuppressProvisioningHint`, `termSize`, `notifyTerminalResize` (existing in package `commands`, from `device_attach.go` and `device_attach_unix.go`/`_windows.go`).

- [ ] **Step 1: Add the client field**

In `go/internal/cli/grpcclient/client.go`, add to the `AgentConnection` struct (after `ContainerService`):

```go
	ShellService        agentpb.WendyShellServiceClient
```

And in `newAgentConnection` (after the `ContainerService:` line):

```go
		ShellService:        agentpb.NewWendyShellServiceClient(conn),
```

- [ ] **Step 2: Create the command**

Create `go/internal/cli/commands/device_shell.go`:

```go
package commands

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"golang.org/x/term"
)

// buildShellStart builds the first HostShell frame. An empty command tells the
// agent to resolve and run the login shell; a command after `--` runs that argv.
func buildShellStart(cmd []string, rows, cols uint32) *agentpb.HostShellRequest {
	return &agentpb.HostShellRequest{RequestType: &agentpb.HostShellRequest_Start_{
		Start: &agentpb.HostShellRequest_Start{
			Command:  cmd,
			TermSize: &agentpb.WindowSize{Rows: rows, Cols: cols},
		},
	}}
}

func shellWinSizeFrame(rows, cols uint32) *agentpb.HostShellRequest {
	return &agentpb.HostShellRequest{RequestType: &agentpb.HostShellRequest_Resize{
		Resize: &agentpb.WindowSize{Rows: rows, Cols: cols},
	}}
}

func newDeviceShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell [-- command...]",
		Short: "Open an interactive shell on the device host",
		Long: "Open a full interactive TTY on the device host (the device's root\n" +
			"filesystem, not a container), running the login shell by default or a\n" +
			"command given after `--`. Uses the existing mTLS/PKI trust; the shell\n" +
			"runs as root.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var shellCmd []string
			if d := cmd.ArgsLenAtDash(); d >= 0 {
				shellCmd = args[d:]
			}
			return runDeviceShell(cmd, shellCmd)
		},
	}
}

func runDeviceShell(cmd *cobra.Command, shellCmd []string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("device shell requires an interactive terminal")
	}

	ctx := cmd.Context()
	conn, err := connectToAgent(ctx, SuppressProvisioningHint())
	if err != nil {
		return err
	}
	defer conn.Close()

	stream, err := conn.ShellService.HostShell(ctx)
	if err != nil {
		return fmt.Errorf("opening shell stream: %w", err)
	}

	// gRPC forbids concurrent SendMsg on one stream; the start frame, resize
	// watcher, and stdin pump all send through this lock.
	var sendMu sync.Mutex
	send := func(req *agentpb.HostShellRequest) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(req)
	}

	rows, cols := termSize(fd)
	if err := send(buildShellStart(shellCmd, rows, cols)); err != nil {
		return fmt.Errorf("sending shell start: %w", err)
	}

	oldState, rawErr := term.MakeRaw(fd)
	if rawErr == nil {
		defer func() { _ = term.Restore(fd, oldState) }()
	}

	// Terminal resize -> resize frames (SIGWINCH on Unix; no-op on Windows).
	winch := make(chan os.Signal, 1)
	stopResize := notifyTerminalResize(winch)
	defer stopResize()
	go func() {
		for range winch {
			r, c := termSize(fd)
			_ = send(shellWinSizeFrame(r, c))
		}
	}()

	// stdin -> stream.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				_ = send(&agentpb.HostShellRequest{
					RequestType: &agentpb.HostShellRequest_StdinData{StdinData: append([]byte(nil), buf[:n]...)},
				})
			}
			if rerr != nil {
				sendMu.Lock()
				_ = stream.CloseSend()
				sendMu.Unlock()
				return
			}
		}
	}()

	// stream -> stdout; exit on the final exit_code frame.
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
		if len(resp.GetStdoutData()) > 0 {
			_, _ = os.Stdout.Write(resp.GetStdoutData())
			continue
		}
		if _, ok := resp.GetResponseType().(*agentpb.HostShellResponse_ExitCode); ok {
			if code := resp.GetExitCode(); code != 0 {
				return fmt.Errorf("remote shell exited with code %d", code)
			}
			return nil
		}
	}
}
```

- [ ] **Step 3: Register the command**

In `go/internal/cli/commands/device.go`, add `newDeviceShellCmd()` to the `addToGroup("manage", ...)` list (next to `newDeviceAttachCmd()`):

```go
		newDeviceInfoCmd(),
		newDeviceAttachCmd(),
		newDeviceShellCmd(),
```

- [ ] **Step 4: Build the CLI and verify the command registers**

Run: `cd go && make build-cli && ./bin/wendy device shell --help`
Expected: build exits 0; help prints the "Open an interactive shell on the device host" long description with `Usage: wendy device shell [-- command...]`.

- [ ] **Step 5: Vet the whole module**

Run: `cd go && go vet ./... && go build ./...`
Expected: exits 0 (no unused imports, no compile errors across CLI + agent).

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/grpcclient/client.go go/internal/cli/commands/device_shell.go go/internal/cli/commands/device.go
git commit -m "feat(cli): add wendy device shell (interactive host TTY over gRPC)"
```

---

## Manual / E2E verification (post-implementation, documented in the PR)

Raw-mode CLI is not unit-testable; verify against a real device:

1. `wendy device shell` into a provisioned device → confirm a root prompt, run `id` (expect `uid=0(root)`), `hostname`, `systemctl status`, `ls /`.
2. Resize the terminal window mid-session → `stty size` (or `tput lines/cols`) reflects the new size.
3. `exit` (or Ctrl-D) → terminal is restored to cooked mode cleanly.
4. `wendy device shell -- /bin/sh -c 'echo hi; exit 4'` → prints `hi`, CLI reports exit code 4.
5. On an image without bash, confirm the login-shell fallback resolves to `/bin/sh` and still works.
6. Check the agent log shows `host shell started` / `host shell completed` audit lines with the exit code.

---

## Self-review notes

- **Spec coverage:** proto service (Task 1) ✓; host PTY spawner + login-shell resolution + `$SHELL`/`sh` fallback (Task 2) ✓; agent handler mirroring ExecContainer with audit logs, PKI-only (no gate) (Tasks 3–4) ✓; CLI near-copy of device_attach with TTY-only guard, reused termSize/SIGWINCH, ShellService client field (Task 5) ✓; root user via `HOME=/root`, cwd `/root` (Task 2) ✓; stdout+stderr merged → single stdout_data (Tasks 1–3) ✓; Windows no-op resize reused from device_attach ✓; Swift not regenerated (noted) ✓; manual E2E ✓.
- **Type consistency:** `HostShellSpawner.Run` signature identical in Task 2 (impl), Task 3 (interface + fake). oneof wrapper names used consistently: `HostShellRequest_Start_` (wrapper) / `HostShellRequest_Start` (message), `HostShellRequest_StdinData`, `HostShellRequest_Resize`, `HostShellResponse_StdoutData`, `HostShellResponse_ExitCode`.
- **No placeholders:** the only "placeholder" is the deliberate Step-1→Step-2 test correction in Task 3, explained inline.
