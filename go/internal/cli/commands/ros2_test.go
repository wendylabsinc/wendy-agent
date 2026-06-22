package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func testROS2Graph() *agentpbv2.GetROS2GraphResponse {
	return &agentpbv2.GetROS2GraphResponse{
		Nodes: []*agentpbv2.ROS2Node{
			{Name: "lidar_driver", Namespace: "/"},
			{Name: "slam_node", Namespace: "/"},
			{Name: "idle_node", Namespace: "/"},
		},
		Publishes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/lidar_driver", Topic: "/scan"},
			{Node: "/lidar_driver", Topic: "/rosout"},
			{Node: "/slam_node", Topic: "/map"},
		},
		Subscribes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/slam_node", Topic: "/scan"},
			{Node: "/slam_node", Topic: "/parameter_events"},
		},
	}
}

func TestRenderROS2GraphASCII(t *testing.T) {
	out := renderROS2GraphASCII(testROS2Graph())
	if !strings.Contains(out, "[/lidar_driver] ──/scan──▶ [/slam_node]") {
		t.Errorf("missing scan edge in:\n%s", out)
	}
	if !strings.Contains(out, "[/slam_node] ──/map──▶ (no subscribers)") {
		t.Errorf("missing dangling map edge in:\n%s", out)
	}
	if strings.Contains(out, "/rosout") || strings.Contains(out, "/parameter_events") {
		t.Errorf("infrastructure topics must be hidden:\n%s", out)
	}
	if !strings.Contains(out, "[/idle_node]") || !strings.Contains(out, "Isolated nodes") {
		t.Errorf("isolated node missing:\n%s", out)
	}
}

func TestRenderROS2GraphASCII_Empty(t *testing.T) {
	out := renderROS2GraphASCII(&agentpbv2.GetROS2GraphResponse{})
	if !strings.Contains(out, "No ROS 2 nodes") {
		t.Errorf("empty graph output = %q", out)
	}
}

func TestRenderROS2GraphDOT(t *testing.T) {
	out := renderROS2GraphDOT(testROS2Graph())
	if !strings.HasPrefix(out, "digraph ros2 {") || !strings.HasSuffix(strings.TrimSpace(out), "}") {
		t.Errorf("not valid DOT shape:\n%s", out)
	}
	if !strings.Contains(out, `"/lidar_driver" -> "/slam_node" [label="/scan"];`) {
		t.Errorf("missing edge in:\n%s", out)
	}
	if !strings.Contains(out, `"/idle_node";`) {
		t.Errorf("missing node declaration in:\n%s", out)
	}
}

func TestExtractROS2BagArchive(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "mybag", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	content := []byte("yaml: data")
	if err := tw.WriteHeader(&tar.Header{Name: "mybag/metadata.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractROS2BagArchive(&buf, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dest, "mybag", "metadata.yaml"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if !bytes.Equal(data, content) {
		t.Errorf("content = %q", data)
	}
}

func TestExtractROS2BagArchive_RejectsTraversal(t *testing.T) {
	for _, evil := range []string{"../escape.txt", "/abs/path.txt"} {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		if err := tw.WriteHeader(&tar.Header{Name: evil, Typeflag: tar.TypeReg, Mode: 0o644, Size: 0}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractROS2BagArchive(&buf, t.TempDir()); err == nil {
			t.Errorf("expected error for archive entry %q", evil)
		}
	}
}

func TestROS2DomainPtr(t *testing.T) {
	if got := ros2DomainPtr(-1); got != nil {
		t.Errorf("ros2DomainPtr(-1) = %v, want nil", got)
	}
	if got := ros2DomainPtr(0); got == nil || *got != 0 {
		t.Errorf("ros2DomainPtr(0) = %v, want 0 (domain 0 is valid)", got)
	}
	if got := ros2DomainPtr(42); got == nil || *got != 42 {
		t.Errorf("ros2DomainPtr(42) = %v", got)
	}
}

// fakeBlockingStream is a bagRecvStream that first returns a fixed payload then
// blocks until its context is cancelled, simulating a live gRPC stream.
type fakeBlockingStream struct {
	ctx     context.Context
	chunks  [][]byte
	sent    int
	blocked chan struct{} // closed when the stream reaches the blocking phase
}

func (f *fakeBlockingStream) Recv() (*agentpbv2.ROS2BagChunk, error) {
	if f.sent < len(f.chunks) {
		chunk := &agentpbv2.ROS2BagChunk{}
		chunk.Data = f.chunks[f.sent]
		f.sent++
		return chunk, nil
	}
	// signal that we are now blocking
	select {
	case <-f.blocked:
	default:
		close(f.blocked)
	}
	// block until context is done
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}

// TestDownloadAndExtractBag_ExtractErrorUnblocksStream verifies WDY-1705 M6:
// when extractROS2BagArchive fails mid-stream the pump goroutine is unblocked
// (call returns, doesn't hang) and no partial directory is left at dest.
func TestDownloadAndExtractBag_ExtractErrorUnblocksStream(t *testing.T) {
	// Build a syntactically valid tar whose first entry has a path-traversal
	// name so that extractROS2BagArchive rejects it immediately after reading
	// the (complete) 512-byte tar header — before requesting more data from the
	// stream. This lets the test stream block on its second Recv while the
	// extractor has already returned an error, proving the pump is unblocked.
	var badTar bytes.Buffer
	tw := tar.NewWriter(&badTar)
	if err := tw.WriteHeader(&tar.Header{
		Name:     "../escape.txt",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := &fakeBlockingStream{
		ctx:     ctx,
		chunks:  [][]byte{badTar.Bytes()},
		blocked: make(chan struct{}),
	}

	dest := t.TempDir()
	finalPath := filepath.Join(dest, "mybag")

	done := make(chan error, 1)
	go func() {
		done <- downloadAndExtractBag(ctx, stream, dest)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error from corrupt tar, got nil")
		}
		// Ensure no partial directory was left at the final path.
		if _, statErr := os.Stat(finalPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("partial bag directory left at %s after extract error", finalPath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downloadAndExtractBag blocked forever — pump goroutine leaked (WDY-1705)")
	}
}

// TestDownloadAndExtractBag_Success verifies the happy path: a valid tar is
// extracted to dest and the final bag directory appears at the correct path.
func TestDownloadAndExtractBag_Success(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	content := []byte("yaml: data")
	headers := []tar.Header{
		{Name: "mybag", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "mybag/metadata.yaml", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))},
	}
	if err := tw.WriteHeader(&headers[0]); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&headers[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	// Wrap the buffer as a single-chunk stream that then returns EOF.
	stream := &fakeEOFStream{data: buf.Bytes()}

	dest := t.TempDir()
	ctx := context.Background()

	if err := downloadAndExtractBag(ctx, stream, dest); err != nil {
		t.Fatalf("downloadAndExtractBag: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "mybag", "metadata.yaml"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// fakeEOFStream sends its entire payload as one chunk then returns io.EOF.
type fakeEOFStream struct {
	data []byte
	sent bool
}

func (f *fakeEOFStream) Recv() (*agentpbv2.ROS2BagChunk, error) {
	if !f.sent {
		f.sent = true
		chunk := &agentpbv2.ROS2BagChunk{}
		chunk.Data = f.data
		return chunk, nil
	}
	return nil, io.EOF
}

// ── drainExecStream tests (WDY-1705 M7) ────────────────────────────────────

// fakeExecStream simulates a grpc.ServerStreamingClient[ROS2ExecOutput].
// msgs is consumed in order; after all msgs are exhausted, Recv returns io.EOF.
type fakeExecStream struct {
	msgs []*agentpbv2.ROS2ExecOutput
	errs []error // parallel slice; non-nil entry returns that error instead
	pos  int
}

func (f *fakeExecStream) Recv() (*agentpbv2.ROS2ExecOutput, error) {
	if f.pos >= len(f.msgs) {
		return nil, io.EOF
	}
	i := f.pos
	f.pos++
	if f.errs != nil && i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	return f.msgs[i], nil
}

func int32ptr(v int32) *int32 { return &v }

// TestDrainExecStream_ExitZero: stream ends with ExitCode=0 → nil error.
func TestDrainExecStream_ExitZero(t *testing.T) {
	stream := &fakeExecStream{
		msgs: []*agentpbv2.ROS2ExecOutput{
			{ExitCode: int32ptr(0)},
		},
	}
	var stdout, stderr bytes.Buffer
	if err := drainExecStream(context.Background(), stream, []string{"node", "list"}, &stdout, &stderr); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// TestDrainExecStream_ExitNonZero: stream ends with ExitCode=2 → error mentioning the code.
func TestDrainExecStream_ExitNonZero(t *testing.T) {
	stream := &fakeExecStream{
		msgs: []*agentpbv2.ROS2ExecOutput{
			{ExitCode: int32ptr(2)},
		},
	}
	var stdout, stderr bytes.Buffer
	err := drainExecStream(context.Background(), stream, []string{"node", "list"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for exit code 2, got nil")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error %q does not mention exit code 2", err.Error())
	}
}

// TestDrainExecStream_NoExitFrame: stream EOFs with no exit-code frame → error about truncation.
func TestDrainExecStream_NoExitFrame(t *testing.T) {
	// Stream sends a stdout chunk but never sends an ExitCode frame.
	stream := &fakeExecStream{
		msgs: []*agentpbv2.ROS2ExecOutput{
			{Stdout: []byte("hello\n")},
		},
	}
	var stdout, stderr bytes.Buffer
	err := drainExecStream(context.Background(), stream, []string{"topic", "echo", "/chatter"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error when no exit-code frame received, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "exit") {
		t.Errorf("error %q does not mention exit status", err.Error())
	}
}

// TestDrainExecStream_OutputBeforeExit: stdout/stderr chunks before the exit frame are forwarded.
func TestDrainExecStream_OutputBeforeExit(t *testing.T) {
	stream := &fakeExecStream{
		msgs: []*agentpbv2.ROS2ExecOutput{
			{Stdout: []byte("out1")},
			{Stderr: []byte("err1")},
			{Stdout: []byte("out2")},
			{ExitCode: int32ptr(0)},
		},
	}
	var stdout, stderr bytes.Buffer
	if err := drainExecStream(context.Background(), stream, []string{"node", "list"}, &stdout, &stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := stdout.String(); got != "out1out2" {
		t.Errorf("stdout = %q, want %q", got, "out1out2")
	}
	if got := stderr.String(); got != "err1" {
		t.Errorf("stderr = %q, want %q", got, "err1")
	}
}

// TestDrainExecStream_DrainsAfterNonZero: trailing output chunks after a non-zero
// exit code in a single frame are still forwarded (exit frame is the last message).
func TestDrainExecStream_DrainsAfterNonZero(t *testing.T) {
	// The agent sends stdout/stderr before the exit frame; this test confirms
	// we don't early-return before draining all output.
	stream := &fakeExecStream{
		msgs: []*agentpbv2.ROS2ExecOutput{
			{Stdout: []byte("before exit")},
			{ExitCode: int32ptr(1)},
		},
	}
	var stdout, stderr bytes.Buffer
	err := drainExecStream(context.Background(), stream, []string{"node", "list"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error for exit code 1, got nil")
	}
	if got := stdout.String(); got != "before exit" {
		t.Errorf("stdout = %q, want %q — output was dropped before drain", got, "before exit")
	}
}

// TestDrainExecStream_ContextCancelledReturnsNil: ctrl-c cancels the ctx; Recv
// returns a gRPC Canceled error (or any error) while ctx.Err() != nil.
// drainExecStream must return nil — NOT an error — even though no exit frame
// was seen.  This locks the ctrl-c clean-stop behaviour (WDY-1705 regression).
func TestDrainExecStream_ContextCancelledReturnsNil(t *testing.T) {
	cancelErr := fmt.Errorf("rpc error: code = Canceled desc = context canceled")
	// errOnFirstRecv is a minimal execRecvStream that always returns an error.
	// This simulates gRPC returning Canceled when the context is cancelled.
	stream := &errOnFirstRecv{err: cancelErr}
	// Pre-cancel the context, exactly as signal.NotifyContext does on ctrl-c.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if err := drainExecStream(ctx, stream, []string{"topic", "echo", "/chatter"}, &stdout, &stderr); err != nil {
		t.Errorf("drainExecStream with cancelled ctx returned %v, want nil (ctrl-c must be a clean stop)", err)
	}
}

// errOnFirstRecv is an execRecvStream that returns a fixed error on every Recv.
type errOnFirstRecv struct{ err error }

func (e *errOnFirstRecv) Recv() (*agentpbv2.ROS2ExecOutput, error) { return nil, e.err }

// TestROS2ExecDeviceFlagNotForwarded guards WDY-1707: --device (and --json)
// appearing after the ros2 command word must be stripped from the forwarded
// args and the extracted value returned so it can select the target device —
// not forwarded to the remote ros2 process which rejects unknown flags.
//
// Implementation note: cobra's SetInterspersed(false) stops all flag parsing
// at the first positional, so post-positional --device always lands in
// Flags().Args() regardless of whether --device is registered locally.
// The fix uses stripWendyExecGlobals to peel off --device/--json in RunE.
func TestROS2ExecDeviceFlagNotForwarded(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantDevice string
		wantJSON   bool
		wantFwd    []string
	}{
		{
			name:       "device after command (space form)",
			args:       []string{"node", "info", "/talker", "--device", "host"},
			wantDevice: "host",
			wantFwd:    []string{"node", "info", "/talker"},
		},
		{
			name:       "device after command (equals form)",
			args:       []string{"node", "info", "/talker", "--device=host"},
			wantDevice: "host",
			wantFwd:    []string{"node", "info", "/talker"},
		},
		{
			name:       "device before command (pre-positional, no strip needed)",
			args:       []string{"node", "info", "/talker"},
			wantDevice: "",
			wantFwd:    []string{"node", "info", "/talker"},
		},
		{
			name:     "json after command",
			args:     []string{"node", "list", "--json"},
			wantJSON: true,
			wantFwd:  []string{"node", "list"},
		},
		{
			name:       "device and ros2 flags coexist",
			args:       []string{"topic", "echo", "/chatter", "--once", "--device", "edge.local"},
			wantDevice: "edge.local",
			wantFwd:    []string{"topic", "echo", "/chatter", "--once"},
		},
		{
			name:       "trailing bare --device (no value) not dropped",
			args:       []string{"node", "list", "--device"},
			wantDevice: "",
			wantFwd:    []string{"node", "list", "--device"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotDevice, gotJSON, gotFwd := stripWendyExecGlobals(tc.args)
			if gotDevice != tc.wantDevice {
				t.Errorf("device = %q, want %q", gotDevice, tc.wantDevice)
			}
			if gotJSON != tc.wantJSON {
				t.Errorf("json = %v, want %v", gotJSON, tc.wantJSON)
			}
			if !reflect.DeepEqual(gotFwd, tc.wantFwd) {
				t.Errorf("forwarded args = %v, want %v", gotFwd, tc.wantFwd)
			}
		})
	}
}

// TestROS2ExecForwardsFlags guards WDY-1553: the raw escape hatch must forward
// --flags meant for ros2 verbatim instead of rejecting them as unknown flags,
// while still parsing wendy's own flags when they precede the ros2 command.
func TestROS2ExecForwardsFlags(t *testing.T) {
	// --once belongs to ros2 and must survive as a positional, not error out.
	cmd := newROS2ExecCmd()
	args := []string{"topic", "echo", "/chatter", "--once"}
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%v) = %v, want nil (ros2 flags must forward verbatim)", args, err)
	}
	if got := cmd.Flags().Args(); !reflect.DeepEqual(got, args) {
		t.Errorf("forwarded args = %v, want %v", got, args)
	}

	// A leading --domain is wendy's own flag: parse it, forward the rest verbatim.
	cmd = newROS2ExecCmd()
	if err := cmd.ParseFlags([]string{"--domain", "5", "topic", "echo", "--once"}); err != nil {
		t.Fatalf("ParseFlags with leading --domain = %v, want nil", err)
	}
	if got, _ := cmd.Flags().GetInt32("domain"); got != 5 {
		t.Errorf("--domain = %d, want 5", got)
	}
	if got, want := cmd.Flags().Args(), []string{"topic", "echo", "--once"}; !reflect.DeepEqual(got, want) {
		t.Errorf("forwarded args = %v, want %v", got, want)
	}
}
