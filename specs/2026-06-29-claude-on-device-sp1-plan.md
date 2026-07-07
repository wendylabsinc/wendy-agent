# Claude-on-device SP1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the Claude Code CLI in an `admin`-entitled container on a Jetson and let it operate & debug the device over the local agent socket — via a wendy-CLI unix-socket transport, a new PTY-capable `ExecContainer` RPC + `wendy device attach`, and a `claude-on-device` app.

**Architecture:** Three layers. (1) The wendy CLI gains a `WENDY_AGENT_SOCKET` transport so every command can talk to the agent over the bind-mounted socket at
`/run/wendy/agent/agent.sock` (host path `/var/lib/wendy/agent-control/agent.sock`; no mTLS). (2) The agent gains a docker-`exec -it`-style `ExecContainer` bidi RPC that runs a process in a container with a containerd PTY + window resize; a new `wendy device attach` subcommand drives it from a raw terminal. (3) A first-party `claude-on-device` app bundles Claude Code + the wendy CLI, declares `{"type":"admin"}`, and persists `/root/.claude`.

**Tech Stack:** Go 1.x (module `github.com/wendylabsinc/wendy`, source under `go/`), gRPC + protoc (regen via `cd go && make proto`), containerd v2 client (`task.Exec`, `cio`), `golang.org/x/term`, Docker/buildx for the app image.

## Global Constraints

- Module root is the **worktree root**; Go source lives under `go/`. Run Go commands from the worktree root as `go test ./go/...`, `go build ./go/...`. Copy verbatim.
- Proto sources live at repo-root `Proto/` (NOT a submodule). Regenerate ONLY via `cd go && make proto` (wraps `bash scripts/generate-proto.sh`, protoc). Never hand-edit files under `go/proto/gen/`.
- PR #1239 is already on `main`: `appconfig.EntitlementAdmin`, `oci.applyAdmin`, `internal/agent/localsocket`, and the `{"type":"admin"}` schema entry all exist. Do NOT re-add them.
- No-mTLS local socket is the entire trust boundary (per #1239). Add NO auth to the socket in SP1.
- TDD: write the failing test first, watch it fail, implement minimally, watch it pass, commit. Frequent commits.
- Connection type returned by all dialers is `*grpcclient.AgentConnection` (NOT `*Conn`); service clients are fields on it (`.AgentService`, `.ContainerService`, …). `newAgentConnection(conn)` (client.go:203) wires them up.
- The CLI connection chokepoint is `connectWithAutoTLSDiagnostics(ctx, plaintextAddr) (*grpcclient.AgentConnection, error, error)` at `go/internal/cli/commands/helpers.go:977`.

---

## File Structure

- `go/internal/cli/grpcclient/client.go` — add `ConnectUnix` (modeled on `Connect`, client.go:68).
- `go/internal/cli/grpcclient/client_unix_test.go` — new; unit test for `ConnectUnix` over a real UDS.
- `go/internal/cli/commands/helpers.go` — add `WENDY_AGENT_SOCKET` early-return in `connectWithAutoTLSDiagnostics` (line 977).
- `go/internal/cli/commands/helpers_socket_test.go` — new; routing test.
- `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto` — add `ExecContainer` RPC + `ExecContainerRequest`/`ExecContainerResponse`.
- `go/internal/agent/containerd/client.go` — add `ExecInContainer` to the concrete `*Client`.
- `go/internal/agent/services/container_service.go` — add the `ExecContainer` gRPC handler + the `ContainerdClient` interface method.
- `go/internal/agent/services/container_service_exec_test.go` — new; handler test with a fake `ContainerdClient`.
- `go/internal/cli/commands/device_attach.go` — new; `wendy device attach` subcommand.
- `go/internal/cli/commands/device_attach_test.go` — new; resize-frame + exit-code plumbing test.
- `go/internal/cli/commands/device.go` — register the `attach` subcommand.
- `Examples/ClaudeOnDevice/` — new app: `wendy.json`, `Dockerfile`, `README.md`.

---

## Task 1: `grpcclient.ConnectUnix` — dial the agent over a unix socket

**Files:**
- Modify: `go/internal/cli/grpcclient/client.go` (add func after `Connect`, ~line 89)
- Test: `go/internal/cli/grpcclient/client_unix_test.go` (create)

**Interfaces:**
- Produces: `func ConnectUnix(ctx context.Context, socketPath string) (*AgentConnection, error)` — dials `socketPath` (a filesystem path) with plain h2c, returns a fully-wired `*AgentConnection`.
- Consumes: existing `newAgentConnection(conn *grpc.ClientConn) *AgentConnection` (client.go:203), `insecure.NewCredentials()` (already imported).

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/grpcclient/client_unix_test.go`:

```go
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
	// Short temp dir: t.TempDir() can exceed the unix-socket sun_path limit
	// (~104 bytes on macOS) -> bind "invalid argument". Matches localsocket_test.go.
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/grpcclient/ -run TestConnectUnix_DialsUDSWithoutTLS -v`
Expected: FAIL to compile — `undefined: ConnectUnix`.

- [ ] **Step 3: Implement `ConnectUnix`**

In `go/internal/cli/grpcclient/client.go`, add after `Connect` (after ~line 89). The contextDialer mirrors the proven tunnel-dialer pattern; the `"passthrough:///"` target keeps gRPC from parsing the socket path as a host.

```go
// ConnectUnix dials the agent over a local unix domain socket with plain h2c
// (no TLS). It is used inside an `admin`-entitled container, where the agent's
// control socket is bind-mounted in and WENDY_AGENT_SOCKET points at it. The
// socket itself is the entire trust boundary (see the admin entitlement); there
// is deliberately no authentication here.
func ConnectUnix(ctx context.Context, socketPath string) (*AgentConnection, error) {
	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	conn, err := grpc.NewClient(
		"passthrough:///unix",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithInitialWindowSize(grpcInitialStreamWindow),
		grpc.WithInitialConnWindowSize(grpcInitialConnWindow),
		grpc.WithReadBufferSize(grpcReadBufferSize),
		grpc.WithWriteBufferSize(grpcWriteBufferSize),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                grpcKeepaliveTime,
			Timeout:             grpcKeepaliveTimeout,
			PermitWithoutStream: false,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to agent at unix:%s: %w", socketPath, err)
	}
	ac := newAgentConnection(conn)
	ac.Host = "unix:" + socketPath
	return ac, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/grpcclient/ -run TestConnectUnix_DialsUDSWithoutTLS -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/grpcclient/client.go go/internal/cli/grpcclient/client_unix_test.go
git commit -m "feat(cli): ConnectUnix — dial the agent over a local unix socket"
```

---

## Task 2: Route the CLI through the socket when `WENDY_AGENT_SOCKET` is set

**Files:**
- Modify: `go/internal/cli/commands/helpers.go:977` (top of `connectWithAutoTLSDiagnostics`)
- Test: `go/internal/cli/commands/helpers_socket_test.go` (create)

**Interfaces:**
- Consumes: `grpcclient.ConnectUnix` (Task 1).
- Produces: behavior — when `WENDY_AGENT_SOCKET` is non-empty, every CLI command dials that socket and skips mTLS/discovery; when unset, behavior is unchanged.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/helpers_socket_test.go`:

```go
package commands

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
)

type versionOnlyAgent struct {
	agentpb.UnimplementedWendyAgentServiceServer
}

func (versionOnlyAgent) GetAgentVersion(context.Context, *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{Version: "sock"}, nil
}

func TestConnectWithAutoTLS_UsesAgentSocketEnv(t *testing.T) {
	// Short temp dir to stay under the unix-socket sun_path limit on macOS
	// (see localsocket_test.go for the convention).
	dir, err := os.MkdirTemp("/tmp", "wsk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "agent.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	agentpb.RegisterWendyAgentServiceServer(srv, versionOnlyAgent{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	t.Setenv("WENDY_AGENT_SOCKET", sock)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// plaintextAddr is deliberately bogus: if the socket path is honored we never
	// touch the network and this unroutable address is never dialed.
	conn, _, err := connectWithAutoTLSDiagnostics(ctx, "203.0.113.1:50051")
	if err != nil {
		t.Fatalf("connectWithAutoTLSDiagnostics: %v", err)
	}
	defer conn.Close()
	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		t.Fatalf("GetAgentVersion: %v", err)
	}
	if resp.GetVersion() != "sock" {
		t.Fatalf("got %q, want sock — did not route through the socket", resp.GetVersion())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run TestConnectWithAutoTLS_UsesAgentSocketEnv -v`
Expected: FAIL — the bogus address is dialed and the call errors / times out (env var not yet honored).

- [ ] **Step 3: Implement the early return**

In `go/internal/cli/commands/helpers.go`, make the FIRST statements of `connectWithAutoTLSDiagnostics` (immediately after the `func ... {` at line 977, before `plaintextAddr = resolveAddrOnce(...)`):

```go
	// An admin-entitled on-device container reaches the agent over its local
	// unix socket (bind-mounted by the `admin` entitlement) with no mTLS. When
	// WENDY_AGENT_SOCKET is set, route every command through it and skip all
	// discovery/mTLS logic. Empty/unset => unchanged off-device behavior.
	if sock := os.Getenv("WENDY_AGENT_SOCKET"); sock != "" {
		conn, err := grpcclient.ConnectUnix(ctx, sock)
		return conn, nil, err
	}
```

(`os` and `grpcclient` are already imported in helpers.go.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/cli/commands/ -run TestConnectWithAutoTLS_UsesAgentSocketEnv -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/helpers.go go/internal/cli/commands/helpers_socket_test.go
git commit -m "feat(cli): route all commands through WENDY_AGENT_SOCKET when set"
```

---

## Task 3: Add the `ExecContainer` RPC to the proto + regenerate

**Files:**
- Modify: `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`
- Regenerate: `go/proto/gen/agentpb/*` (via `make proto`, do not hand-edit)

**Interfaces:**
- Produces: `WendyContainerService.ExecContainer(stream ExecContainerRequest) returns (stream ExecContainerResponse)`; messages `ExecContainerRequest{ oneof: ExecStart start | bytes stdin_data | WindowSize resize }`, `ExecStart{ string app_name; repeated string command; bool tty; WindowSize term_size }`, `WindowSize{ uint32 rows; uint32 cols }`, `ExecContainerResponse{ oneof: bytes stdout_data | bytes stderr_data | int32 exit_code }`. Generated Go: `agentpb.ExecContainerRequest`, `agentpb.ExecContainerRequest_Start`, `agentpb.ExecStart`, `agentpb.WindowSize`, `agentpb.ExecContainerResponse`, client `WendyContainerServiceClient.ExecContainer(ctx) (grpc.BidiStreamingClient[...], error)`, server method on `WendyContainerServiceServer`.

- [ ] **Step 1: Add the RPC + messages to the proto**

In `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`, add inside the `service WendyContainerService { ... }` block (after the `AttachContainer` line):

```proto
    // ExecContainer runs a process inside an existing container with an
    // interactive PTY (docker `exec -it` style). The first client message MUST
    // be ExecStart; subsequent messages carry stdin or window resizes. The
    // server streams stdout/stderr and a final exit_code.
    rpc ExecContainer(stream ExecContainerRequest) returns (stream ExecContainerResponse);
```

Then add the messages near `AttachContainerRequest` (around line 177):

```proto
message WindowSize {
    uint32 rows = 1;
    uint32 cols = 2;
}

message ExecContainerRequest {
    message ExecStart {
        string app_name = 1;
        repeated string command = 2;
        bool tty = 3;
        WindowSize term_size = 4;
    }
    oneof request_type {
        ExecStart start = 1;
        bytes stdin_data = 2;
        WindowSize resize = 3;
    }
}

message ExecContainerResponse {
    oneof response_type {
        bytes stdout_data = 1;
        bytes stderr_data = 2;
        int32 exit_code = 3;
    }
}
```

- [ ] **Step 2: Regenerate**

Run: `cd go && make proto && cd ..`
Expected: regenerates `go/proto/gen/agentpb/`; no errors.

- [ ] **Step 3: Verify it compiles and the symbols exist**

Run: `go build ./go/proto/... && go vet ./go/proto/gen/agentpb/ 2>&1 | head`
Then confirm the generated client method exists:
Run: `grep -n "ExecContainer(ctx context.Context" go/proto/gen/agentpb/wendy_agent_v1_container_service_grpc.pb.go`
Expected: prints the generated `ExecContainer` client method signature.

- [ ] **Step 4: Commit**

```bash
git add Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto go/proto/gen/agentpb/
git commit -m "feat(proto): add ExecContainer bidi RPC (PTY exec + resize)"
```

---

## Task 4: `ContainerdClient.ExecInContainer` — containerd PTY exec

**Files:**
- Modify: `go/internal/agent/services/container_service.go` (extend the `ContainerdClient` interface)
- Modify: `go/internal/agent/containerd/client.go` (implement on `*Client`)

**Interfaces:**
- Produces: interface method (on the `ContainerdClient` interface that `ContainerService` consumes) and concrete impl:
  `ExecInContainer(ctx context.Context, appName string, command []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, resize <-chan [2]uint32) (int, error)` — runs `command` in app `appName`'s container; if `tty`, allocates a console (stderr unused, merged into stdout) and applies `resize` events (`[rows, cols]`) via the process's `Resize`; returns the exit code.
- Consumes: containerd `task.Exec`, `cio.NewCreator`, `cio.WithStreams`, `cio.WithTerminal`, `oci`/`specs.Process` (already imported in containerd package — see `ros2.go:847`).

- [ ] **Step 1: Add the interface method (failing build)**

In `go/internal/agent/services/container_service.go`, find the `ContainerdClient` interface (the methods `ContainerService` calls, incl. `StartContainerWithStdin`) and add:

```go
	// ExecInContainer runs command in the named app's running container. When
	// tty is true a PTY is allocated (stderr is merged into stdout) and resize
	// events ([rows, cols]) are applied to the process. Returns the exit code.
	ExecInContainer(ctx context.Context, appName string, command []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, resize <-chan [2]uint32) (int, error)
```

- [ ] **Step 2: Verify the build fails**

Run: `go build ./go/internal/agent/...`
Expected: FAIL — `*Client` does not implement `ContainerdClient` (missing `ExecInContainer`).

- [ ] **Step 3: Implement on `*Client`**

In `go/internal/agent/containerd/client.go`, add (model on the existing `task.Exec` in `ros2.go:847` and `StartContainerWithStdin` at line 1188). Adjust the container/task lookup to match how the package already loads a running task for `appName` (reuse the same helper `StartContainerWithStdin` uses to resolve the container/task; e.g. `c.loadRunningTask(ctx, appName)` if present, else replicate its `container.Task(ctx, nil)` lookup):

```go
func (c *Client) ExecInContainer(ctx context.Context, appName string, command []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, resize <-chan [2]uint32) (int, error) {
	task, err := c.runningTaskForApp(ctx, appName) // resolve the app's running task (reuse existing lookup)
	if err != nil {
		return -1, err
	}

	spec, err := task.Spec(ctx)
	if err != nil {
		return -1, fmt.Errorf("load container spec: %w", err)
	}
	pspec := spec.Process
	pspec.Terminal = tty
	pspec.Args = command

	var ioCreator cio.Creator
	if tty {
		ioCreator = cio.NewCreator(cio.WithStreams(stdin, stdout, nil), cio.WithTerminal)
	} else {
		ioCreator = cio.NewCreator(cio.WithStreams(stdin, stdout, stderr))
	}

	execID := fmt.Sprintf("exec-%d-%d", time.Now().UnixNano(), execCounter.Add(1))
	proc, err := task.Exec(ctx, execID, pspec, ioCreator)
	if err != nil {
		return -1, fmt.Errorf("exec in container: %w", err)
	}
	defer func() { _, _ = proc.Delete(ctx, containerd.WithProcessKill) }()

	statusC, err := proc.Wait(ctx)
	if err != nil {
		return -1, fmt.Errorf("wait on exec: %w", err)
	}
	if err := proc.Start(ctx); err != nil {
		return -1, fmt.Errorf("start exec: %w", err)
	}

	if tty && resize != nil {
		go func() {
			for sz := range resize {
				_ = proc.Resize(ctx, sz[1], sz[0]) // Resize(width=cols, height=rows)
			}
		}()
	}

	select {
	case st := <-statusC:
		code, _, _ := st.Result()
		return int(code), st.Error()
	case <-ctx.Done():
		_ = proc.Kill(ctx, syscall.SIGKILL)
		return -1, ctx.Err()
	}
}
```

Add at package scope near the other counters (mirroring `ros2ExecCounter`):

```go
var execCounter atomic.Uint64
```

Ensure imports include `syscall` and `sync/atomic` (already used by `ros2.go`; add to client.go if absent).

> NOTE: `runningTaskForApp` is a stand-in for the lookup `StartContainerWithStdin` already performs. During implementation, factor that lookup out of `StartContainerWithStdin` into a small helper and call it from both, OR inline the same `c.client.LoadContainer`/`container.Task` sequence. Do not invent a new resolution scheme.

- [ ] **Step 4: Build the agent**

Run: `go build ./go/internal/agent/... ./go/cmd/wendy-agent/`
Expected: PASS (compiles). The concrete PTY behavior is covered by the on-device smoke (Task 8); unit coverage of the handler that drives this is Task 5 via a fake.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/containerd/client.go go/internal/agent/services/container_service.go
git commit -m "feat(agent): ExecInContainer — containerd PTY exec with resize"
```

---

## Task 5: `ExecContainer` gRPC handler on `ContainerService`

**Files:**
- Modify: `go/internal/agent/services/container_service.go` (add the `ExecContainer` method)
- Test: `go/internal/agent/services/container_service_exec_test.go` (create)

**Interfaces:**
- Consumes: `ContainerdClient.ExecInContainer` (Task 4); generated `agentpb.ExecContainerRequest/Response` (Task 3).
- Produces: `func (s *ContainerService) ExecContainer(stream grpc.BidiStreamingServer[agentpb.ExecContainerRequest, agentpb.ExecContainerResponse]) error`.

- [ ] **Step 1: Write the failing test (fake ContainerdClient)**

Create `go/internal/agent/services/container_service_exec_test.go`. The fake echoes stdin to stdout and records the first resize, then returns exit code 7:

```go
package services

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"go.uber.org/zap"
)

type fakeExecContainerd struct {
	ContainerdClient // embed so we only override ExecInContainer
	gotApp           string
	gotCmd           []string
	gotTTY           bool
	gotResize        [2]uint32
}

func (f *fakeExecContainerd) ExecInContainer(ctx context.Context, appName string, command []string, tty bool, stdin io.Reader, stdout, stderr io.Writer, resize <-chan [2]uint32) (int, error) {
	f.gotApp, f.gotCmd, f.gotTTY = appName, command, tty
	// Drain one resize event if present.
	go func() {
		if sz, ok := <-resize; ok {
			f.gotResize = sz
		}
	}()
	// Echo stdin -> stdout until EOF.
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

// mockExecStream is a minimal in-memory BidiStreamingServer.
type mockExecStream struct {
	grpc_ServerStream
	ctx     context.Context
	in      chan *agentpb.ExecContainerRequest
	out     chan *agentpb.ExecContainerResponse
}

func (m *mockExecStream) Context() context.Context { return m.ctx }
func (m *mockExecStream) Recv() (*agentpb.ExecContainerRequest, error) {
	req, ok := <-m.in
	if !ok {
		return nil, io.EOF
	}
	return req, nil
}
func (m *mockExecStream) Send(resp *agentpb.ExecContainerResponse) error {
	m.out <- resp
	return nil
}

func TestExecContainer_EchoesStdinAndReturnsExit(t *testing.T) {
	fake := &fakeExecContainerd{}
	svc := &ContainerService{logger: zap.NewNop(), containerd: fake}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := &mockExecStream{
		ctx: ctx,
		in:  make(chan *agentpb.ExecContainerRequest, 4),
		out: make(chan *agentpb.ExecContainerResponse, 16),
	}

	stream.in <- &agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Start{
		Start: &agentpb.ExecContainerRequest_ExecStart{
			AppName:  "myapp",
			Command:  []string{"claude"},
			Tty:      true,
			TermSize: &agentpb.WindowSize{Rows: 40, Cols: 120},
		},
	}}
	stream.in <- &agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_StdinData{StdinData: []byte("hi")}}
	stream.in <- &agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Resize{Resize: &agentpb.WindowSize{Rows: 50, Cols: 200}}}
	close(stream.in) // EOF -> stdin pipe closes -> fake returns

	errCh := make(chan error, 1)
	go func() { errCh <- svc.ExecContainer(stream) }()

	var sawStdout bool
	var exit int32 = -1
	timeout := time.After(5 * time.Second)
	for {
		select {
		case resp := <-stream.out:
			if d := resp.GetStdoutData(); len(d) > 0 {
				sawStdout = true
			}
			if _, ok := resp.GetResponseType().(*agentpb.ExecContainerResponse_ExitCode); ok {
				exit = resp.GetExitCode()
			}
		case err := <-errCh:
			if err != nil {
				t.Fatalf("ExecContainer: %v", err)
			}
			if fake.gotApp != "myapp" || fake.gotCmd[0] != "claude" || !fake.gotTTY {
				t.Fatalf("start args not forwarded: %+v", fake)
			}
			if !sawStdout {
				t.Fatalf("stdout echo not streamed back")
			}
			if exit != 7 {
				t.Fatalf("exit code = %d, want 7", exit)
			}
			return
		case <-timeout:
			t.Fatal("timed out")
		}
	}
}
```

> Implementation note: `grpc_ServerStream` above stands for an embedded `grpc.ServerStream` (embed it to satisfy the unused interface methods). Name the embedded field accordingly when implementing — adjust to the real embedding the package convention uses for stream mocks (see how other `*_test.go` in this package mock streams, e.g. attach/run tests).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestExecContainer_EchoesStdinAndReturnsExit -v`
Expected: FAIL — `svc.ExecContainer` undefined.

- [ ] **Step 3: Implement the handler**

In `go/internal/agent/services/container_service.go`, add (model the stdin pipe + output goroutine on `AttachContainer` at line 571):

```go
func (s *ContainerService) ExecContainer(stream grpc.BidiStreamingServer[agentpb.ExecContainerRequest, agentpb.ExecContainerResponse]) error {
	first, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "missing first exec message")
	}
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil || start.GetAppName() == "" {
		return status.Error(codes.InvalidArgument, "first message must be ExecStart with app_name")
	}
	command := start.GetCommand()
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}

	ctx := stream.Context()
	stdinR, stdinW := io.Pipe()
	defer stdinR.Close()

	resize := make(chan [2]uint32, 8)
	if ts := start.GetTermSize(); ts != nil {
		resize <- [2]uint32{ts.GetRows(), ts.GetCols()}
	}

	// Forward stdin + resize frames from the client.
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
				default:
				}
			}
		}
	}()

	stdout := &execWriter{stream: stream, stderr: false}
	stderr := &execWriter{stream: stream, stderr: true}

	code, err := s.containerd.ExecInContainer(ctx, start.GetAppName(), command, start.GetTty(), stdinR, stdout, stderr, resize)
	if err != nil {
		return status.Errorf(codes.Internal, "exec failed: %v", err)
	}
	return stream.Send(&agentpb.ExecContainerResponse{
		ResponseType: &agentpb.ExecContainerResponse_ExitCode{ExitCode: int32(code)},
	})
}

// execWriter adapts an output stream to io.Writer for the exec PTY.
type execWriter struct {
	stream grpc.BidiStreamingServer[agentpb.ExecContainerRequest, agentpb.ExecContainerResponse]
	stderr bool
}

func (w *execWriter) Write(p []byte) (int, error) {
	buf := append([]byte(nil), p...) // copy: the PTY reuses its buffer
	var resp *agentpb.ExecContainerResponse
	if w.stderr {
		resp = &agentpb.ExecContainerResponse{ResponseType: &agentpb.ExecContainerResponse_StderrData{StderrData: buf}}
	} else {
		resp = &agentpb.ExecContainerResponse{ResponseType: &agentpb.ExecContainerResponse_StdoutData{StdoutData: buf}}
	}
	if err := w.stream.Send(resp); err != nil {
		return 0, err
	}
	return len(p), nil
}
```

Ensure `io`, `codes`, `status`, and the generated `agentpb` are imported (they already are in this file for `AttachContainer`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./go/internal/agent/services/ -run TestExecContainer_EchoesStdinAndReturnsExit -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/container_service.go go/internal/agent/services/container_service_exec_test.go
git commit -m "feat(agent): ExecContainer gRPC handler driving the PTY exec"
```

---

## Task 6: `wendy device attach` subcommand

**Files:**
- Create: `go/internal/cli/commands/device_attach.go`
- Modify: `go/internal/cli/commands/device.go` (register the subcommand)
- Test: `go/internal/cli/commands/device_attach_test.go` (create)

**Interfaces:**
- Consumes: `conn.ContainerService.ExecContainer(ctx)` (generated client, Task 3); the existing client helper the other `device` subcommands use (`connectToAgent`/`resolveTarget` — match the sibling subcommand in device.go).
- Produces: cobra command `attach <app> [-- cmd...]`; a testable helper `buildExecStart(app string, args []string, rows, cols uint32) *agentpb.ExecContainerRequest` and `winSizeFrame(rows, cols uint32) *agentpb.ExecContainerRequest`.

- [ ] **Step 1: Write the failing test (pure frame builders)**

Create `go/internal/cli/commands/device_attach_test.go`:

```go
package commands

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestBuildExecStart_DefaultsAndCmd(t *testing.T) {
	// No explicit command -> defaults to claude (the app's purpose).
	req := buildExecStart("claude-on-device", nil, 40, 120)
	st := req.GetStart()
	if st == nil {
		t.Fatal("expected ExecStart")
	}
	if st.GetAppName() != "claude-on-device" {
		t.Fatalf("app = %q", st.GetAppName())
	}
	if !st.GetTty() {
		t.Fatal("tty should default true")
	}
	if len(st.GetCommand()) != 1 || st.GetCommand()[0] != "claude" {
		t.Fatalf("command = %v, want [claude]", st.GetCommand())
	}
	if st.GetTermSize().GetRows() != 40 || st.GetTermSize().GetCols() != 120 {
		t.Fatalf("term size = %v", st.GetTermSize())
	}

	// Explicit command is forwarded verbatim.
	req2 := buildExecStart("app", []string{"bash", "-l"}, 10, 20)
	if got := req2.GetStart().GetCommand(); len(got) != 2 || got[0] != "bash" || got[1] != "-l" {
		t.Fatalf("command = %v, want [bash -l]", got)
	}
}

func TestWinSizeFrame(t *testing.T) {
	f := winSizeFrame(50, 200)
	if f.GetResize().GetRows() != 50 || f.GetResize().GetCols() != 200 {
		t.Fatalf("resize = %v", f.GetResize())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run 'TestBuildExecStart_DefaultsAndCmd|TestWinSizeFrame' -v`
Expected: FAIL — `buildExecStart`/`winSizeFrame` undefined.

- [ ] **Step 3: Implement the subcommand + helpers**

Create `go/internal/cli/commands/device_attach.go`:

```go
package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"golang.org/x/term"
)

func buildExecStart(app string, cmd []string, rows, cols uint32) *agentpb.ExecContainerRequest {
	if len(cmd) == 0 {
		cmd = []string{"claude"}
	}
	return &agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Start{
		Start: &agentpb.ExecContainerRequest_ExecStart{
			AppName:  app,
			Command:  cmd,
			Tty:      true,
			TermSize: &agentpb.WindowSize{Rows: rows, Cols: cols},
		},
	}}
}

func winSizeFrame(rows, cols uint32) *agentpb.ExecContainerRequest {
	return &agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_Resize{
		Resize: &agentpb.WindowSize{Rows: rows, Cols: cols},
	}}
}

func termSize(fd int) (rows, cols uint32) {
	w, h, err := term.GetSize(fd)
	if err != nil || w == 0 || h == 0 {
		return 24, 80
	}
	return uint32(h), uint32(w)
}

func newDeviceAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <app> [-- command...]",
		Short: "Attach an interactive PTY to a running app's container",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			app := args[0]
			var execCmd []string
			if cmd.ArgsLenAtDash() >= 0 {
				execCmd = args[cmd.ArgsLenAtDash():]
			}
			return runDeviceAttach(ctx, app, execCmd)
		},
	}
}

func runDeviceAttach(ctx context.Context, app string, execCmd []string) error {
	target, err := resolveTarget(ctx) // match sibling device subcommands
	if err != nil {
		return err
	}
	defer target.Close()
	conn := target.Agent
	if conn == nil {
		return errors.New("no agent connection")
	}

	stream, err := conn.ContainerService.ExecContainer(ctx)
	if err != nil {
		return fmt.Errorf("opening exec stream: %w", err)
	}

	fd := int(os.Stdin.Fd())
	rows, cols := termSize(fd)
	if err := stream.Send(buildExecStart(app, execCmd, rows, cols)); err != nil {
		return err
	}

	var oldState *term.State
	if term.IsTerminal(fd) {
		if oldState, err = term.MakeRaw(fd); err == nil {
			defer func() { _ = term.Restore(fd, oldState) }()
		}
	}

	// SIGWINCH -> resize frames.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			r, c := termSize(fd)
			_ = stream.Send(winSizeFrame(r, c))
		}
	}()

	// stdin -> stream.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := os.Stdin.Read(buf)
			if n > 0 {
				_ = stream.Send(&agentpb.ExecContainerRequest{RequestType: &agentpb.ExecContainerRequest_StdinData{StdinData: append([]byte(nil), buf[:n]...)}})
			}
			if rerr != nil {
				_ = stream.CloseSend()
				return
			}
		}
	}()

	// stream -> stdout/stderr; exit on exit_code.
	for {
		resp, rerr := stream.Recv()
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
		switch {
		case len(resp.GetStdoutData()) > 0:
			_, _ = os.Stdout.Write(resp.GetStdoutData())
		case len(resp.GetStderrData()) > 0:
			_, _ = os.Stderr.Write(resp.GetStderrData())
		default:
			if _, ok := resp.GetResponseType().(*agentpb.ExecContainerResponse_ExitCode); ok {
				if code := resp.GetExitCode(); code != 0 {
					return fmt.Errorf("remote exited with code %d", code)
				}
				return nil
			}
		}
	}
}
```

> `resolveTarget`/`target.Agent`/`target.Close()` must match the exact helper the other `device` subcommands use — open `device.go` and copy the sibling pattern (some use `connectToAgent`). Use whatever the neighbors use; the rest of this function is unchanged.

- [ ] **Step 4: Register the subcommand**

In `go/internal/cli/commands/device.go`, where sibling subcommands are added to the `device` command (the `AddCommand`/`addToGroup` block), add `newDeviceAttachCmd()` to the appropriate group (the "manage" group, alongside `apps`).

- [ ] **Step 5: Run tests + build**

Run: `go test ./go/internal/cli/commands/ -run 'TestBuildExecStart_DefaultsAndCmd|TestWinSizeFrame' -v && go build ./go/...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/device_attach.go go/internal/cli/commands/device_attach_test.go go/internal/cli/commands/device.go
git commit -m "feat(cli): wendy device attach — interactive PTY into a container"
```

---

## Task 7: The `claude-on-device` app (image + admin entitlement)

**Files:**
- Create: `Examples/ClaudeOnDevice/wendy.json`
- Create: `Examples/ClaudeOnDevice/Dockerfile`
- Create: `Examples/ClaudeOnDevice/README.md`
- Test: `go/internal/shared/appconfig/appconfig_test.go` (add one case)

**Interfaces:**
- Consumes: the `admin` entitlement validated by `appconfig` (already on main).

- [ ] **Step 1: Write the failing test (admin app parses & validates)**

In `go/internal/shared/appconfig/appconfig_test.go`, add (mirror the existing valid-entitlement cases):

```go
func TestClaudeOnDeviceConfig_AdminEntitlementValidates(t *testing.T) {
	raw := []byte(`{
		"appId": "sh.wendy.examples.claude-on-device",
		"version": "1.0.0",
		"language": "custom",
		"platform": "linux",
		"entitlements": [{"type": "admin"}]
	}`)
	cfg, err := Parse(raw) // use the package's real parse entrypoint
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var hasAdmin bool
	for _, e := range cfg.Entitlements {
		if e.Type == EntitlementAdmin {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Fatal("admin entitlement not parsed")
	}
}
```

> Adjust `Parse`/`Validate`/`Entitlements` to the appconfig package's actual API (open appconfig.go to confirm the parse entrypoint and the config struct field names). The assertion content stays the same.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./go/internal/shared/appconfig/ -run TestClaudeOnDeviceConfig_AdminEntitlementValidates -v`
Expected: FAIL until the app config matches a valid shape (or compile error if API names differ — fix names, not the intent).

- [ ] **Step 3: Create the app files**

`Examples/ClaudeOnDevice/wendy.json`:

```json
{
  "appId": "sh.wendy.examples.claude-on-device",
  "version": "1.0.0",
  "language": "custom",
  "platform": "linux",
  "entitlements": [
    { "type": "admin" }
  ]
}
```

`Examples/ClaudeOnDevice/Dockerfile` (arm64 base; bundles Claude Code + the wendy CLI built from this branch; idle entrypoint so `wendy device attach` execs `claude` on demand):

```dockerfile
# Built/deployed onto an arm64 Jetson. Cross-built from an amd64 host via buildx.
FROM node:22-bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends git ca-certificates ripgrep \
  && rm -rf /var/lib/apt/lists/*

# Claude Code CLI.
RUN npm install -g @anthropic-ai/claude-code

# The wendy CLI built from this branch (understands WENDY_AGENT_SOCKET). The
# binary is staged into the build context as `wendy-linux-arm64` before build.
COPY wendy-linux-arm64 /usr/local/bin/wendy
RUN chmod +x /usr/local/bin/wendy

# Persistent across restarts via the app's volume mounts (see README): /root/.claude
# holds the OAuth token + config; /workspace is Claude's working dir.
WORKDIR /workspace

# Idle so the container stays up; `wendy device attach claude-on-device` execs
# `claude` into it with a fresh PTY.
CMD ["sleep", "infinity"]
```

`Examples/ClaudeOnDevice/README.md`:

```markdown
# Claude-on-device

Runs the Claude Code CLI inside an `admin`-entitled container on a WendyOS device
so the device can operate and debug itself over the local agent socket.

## ⚠️ Security: `admin` is a full-control grant
The `admin` entitlement bind-mounts the agent's control socket into this container
with **no authentication** — anything here can start/stop/**delete** any app, read
all telemetry, exec into any container, and trigger OS/agent updates (i.e. brick or
wipe the device). Deploy ONLY to trusted, first-party devices. The OAuth token also
lives on the device volume.

## Build & deploy (from an amd64 dev host)
1. Cross-build the wendy CLI for arm64 and stage it into this dir as
   `wendy-linux-arm64` (so the in-container `wendy` understands `WENDY_AGENT_SOCKET`).
2. Deploy the agent built from this branch to the device first
   (`wendy device update --binary ...`) — older agents lack `ExecContainer`.
3. `wendy run --yes --build-type docker --device <jetson-hostname>` from this dir.

## Log in & use
```
wendy device attach claude-on-device
```
On first run `claude` prints an OAuth URL + code; approve it in your laptop browser,
paste the code back. The token persists to `/root/.claude`. Then drive the device:
`wendy device info`, `wendy device apps`, `wendy device attach <other-app>`, etc.
(the in-container `wendy` is pre-pointed at the local socket).
```

- [ ] **Step 4: Run the test + validate the app config with the CLI**

Run: `go test ./go/internal/shared/appconfig/ -run TestClaudeOnDeviceConfig_AdminEntitlementValidates -v`
Expected: PASS.
Also (sanity, optional, requires a built CLI): `wendy json validate Examples/ClaudeOnDevice/wendy.json` (or the repo's equivalent schema-check) — expect no errors.

- [ ] **Step 5: Commit**

```bash
git add Examples/ClaudeOnDevice/ go/internal/shared/appconfig/appconfig_test.go
git commit -m "feat(examples): claude-on-device app (admin entitlement + Claude Code)"
```

---

## Task 8: Docs blast-radius note + full-suite gate + on-device smoke

**Files:**
- Modify: `go/internal/cli/assets/docs/entitlements.md` (extend the `admin` entry with the Claude-on-device usage + blast radius — append, don't duplicate #1239's existing text)

**Interfaces:** none (docs + verification).

- [ ] **Step 1: Extend the entitlement docs**

In `go/internal/cli/assets/docs/entitlements.md`, under the existing `admin` section, append a short paragraph:

```markdown
A first-party use of `admin` is the **claude-on-device** app (`Examples/ClaudeOnDevice`):
the Claude Code CLI runs in the container and drives the device through the local
socket. Because `admin` is unauthenticated full local control, the in-container agent
(human or AI) can delete apps and trigger OS/agent updates — deploy only to trusted
devices.
```

- [ ] **Step 2: Full unit-test gate**

Run: `go test ./go/...`
Expected: PASS (no regressions). Investigate and fix any failures attributable to these changes before proceeding.

- [ ] **Step 3: Lint/vet**

Run: `go vet ./go/... && (cd go && gofmt -l $(git -C .. diff --name-only main -- '*.go'))`
Expected: clean (no vet errors; gofmt lists nothing).

- [ ] **Step 4: On-device smoke (manual — record results in the PR)**

This is the real proof (PR #1239 did the same on an Orin):
1. Cross-build agent + CLI for arm64; `wendy device update --binary` the new agent to a Jetson.
2. Stage `wendy-linux-arm64` into `Examples/ClaudeOnDevice/`, then `wendy run --yes --build-type docker --device <host>` from that dir.
3. `wendy device attach claude-on-device` → a PTY opens; run `claude`, complete the OAuth paste-flow; confirm the token persists across an app restart.
4. Inside the attach session: `wendy device info` returns real device data over the socket; `wendy device apps` lists apps; `wendy device attach <other-app>` execs into another container.
5. Resize your terminal and confirm the remote TUI reflows (resize frames work).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/assets/docs/entitlements.md
git commit -m "docs: claude-on-device usage + admin blast-radius note"
```

---

## Self-Review

**Spec coverage** (each design section → task):
- §A CLI socket transport → Tasks 1–2. ✓
- §B agent exec + PTY + `wendy device attach` → Tasks 3 (proto), 4 (containerd PTY), 5 (handler), 6 (CLI). ✓
- §C `claude-on-device` app → Task 7. ✓
- §D auth flow → Task 7 (volume + README paste-flow) + Task 8 smoke step 3. ✓
- §Security model (docs-only) → Task 7 README + Task 8 entitlements.md. ✓
- §Testing → unit tests in Tasks 1,2,5,6,7; full gate Task 8 step 2; on-device smoke Task 8 step 4. ✓
- §Phasing 1–5 → Tasks map 1:1 onto the design's phasing. ✓

**Placeholder scan:** No "TBD"/"add error handling"/"similar to". The few `>`-NOTE callouts (running-task lookup in Task 4, stream-mock embedding in Task 5, `resolveTarget` vs `connectToAgent` in Task 6, appconfig API names in Task 7) are explicit "match the existing sibling pattern — confirm the exact name in this file" instructions, not deferred work; each names the file and the concrete pattern to copy.

**Type consistency:** `*grpcclient.AgentConnection` used throughout (not `*Conn`). `ExecInContainer(ctx, app, command, tty, stdin, stdout, stderr, resize<-chan [2]uint32) (int, error)` is identical in Task 4 (interface + impl) and Task 5 (fake + handler call). Generated names `agentpb.ExecContainerRequest_Start`, `agentpb.ExecContainerRequest_ExecStart`, `agentpb.WindowSize`, `agentpb.ExecContainerResponse_ExitCode/StdoutData/StderrData` consistent across Tasks 3, 5, 6. `buildExecStart`/`winSizeFrame` signatures match between Task 6 test and impl.
