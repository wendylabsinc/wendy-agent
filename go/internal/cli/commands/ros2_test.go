package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
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
