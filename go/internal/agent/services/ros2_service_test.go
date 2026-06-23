package services

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// fakeROS2Runtime scripts ExecROS2 responses keyed by the joined args.
type fakeROS2Runtime struct {
	sidecar   ROS2Sidecar
	sidecars  []ROS2Sidecar // when set, EnsureROS2Sidecars returns these (mixed-RMW tests)
	ensureErr error
	verifyErr error
	// outputs maps "node list" → stdout. Missing keys exit 1.
	outputs map[string]string
	// execFn, when set, overrides the outputs map entirely.
	execFn func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error)
	calls  []ROS2ExecOptions
}

func (f *fakeROS2Runtime) FindROS2Containers(context.Context) ([]ROS2Target, error) {
	return nil, nil
}

func (f *fakeROS2Runtime) EnsureROS2Sidecars(context.Context) ([]ROS2Sidecar, error) {
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if len(f.sidecars) > 0 {
		return f.sidecars, nil
	}
	return []ROS2Sidecar{f.sidecar}, nil
}

func (f *fakeROS2Runtime) StopROS2Sidecar(context.Context) error { return nil }

func (f *fakeROS2Runtime) VerifyROS2Sidecar(context.Context) error { return f.verifyErr }

func (f *fakeROS2Runtime) ExecROS2(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
	f.calls = append(f.calls, opts)
	if f.execFn != nil {
		return f.execFn(ctx, opts, stdout, stderr)
	}
	key := strings.Join(opts.Args, " ")
	out, ok := f.outputs[key]
	if !ok {
		fmt.Fprintf(stderr, "unknown command: ros2 %s", key)
		return 1, nil
	}
	_, _ = io.WriteString(stdout, out)
	return 0, nil
}

func newTestROS2Service(t *testing.T, runtime ROS2Runtime, bagDir string) *ROS2Service {
	t.Helper()
	return NewROS2Service(zaptest.NewLogger(t), runtime, bagDir)
}

func TestROS2Service_ListNodes(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 7},
		outputs: map[string]string{"node list": "/talker\n/camera/driver\n"},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("got %d nodes, want 2", len(resp.GetNodes()))
	}
	if rt.calls[0].DomainID != 7 {
		t.Errorf("exec domain = %d, want sidecar default 7", rt.calls[0].DomainID)
	}
}

// TestROS2Service_ListNodes_MergesPerRMW verifies a mixed-RMW device merges the
// nodes from every RMW sidecar and tags each with its RMW (WDY-1594).
func TestROS2Service_ListNodes_MergesPerRMW(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecars: []ROS2Sidecar{
			{Name: "sc-cyc", Distro: "humble", DomainID: 42, RMW: "rmw_cyclonedds_cpp"},
			{Name: "sc-fast", Distro: "humble", DomainID: 42, RMW: "rmw_fastrtps_cpp"},
		},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			switch opts.SidecarName {
			case "sc-cyc":
				io.WriteString(stdout, "/talker\n")
			case "sc-fast":
				io.WriteString(stdout, "/listener\n")
			}
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("got %d nodes, want 2 (merged across RMWs)", len(resp.GetNodes()))
	}
	byRMW := map[string]string{}
	for _, n := range resp.GetNodes() {
		byRMW[n.GetRmw()] = n.GetName()
	}
	if byRMW["rmw_cyclonedds_cpp"] != "talker" || byRMW["rmw_fastrtps_cpp"] != "listener" {
		t.Errorf("merged nodes not tagged by RMW: %+v", byRMW)
	}
}

// twoSidecarRuntime is a fake with one CycloneDDS and one FastRTPS sidecar.
func twoSidecarRuntime(execFn func(context.Context, ROS2ExecOptions, io.Writer, io.Writer) (int, error)) *fakeROS2Runtime {
	return &fakeROS2Runtime{
		sidecars: []ROS2Sidecar{
			{Name: "sc-cyc", Distro: "humble", DomainID: 42, RMW: "rmw_cyclonedds_cpp"},
			{Name: "sc-fast", Distro: "humble", DomainID: 42, RMW: "rmw_fastrtps_cpp"},
		},
		execFn: execFn,
	}
}

// TestROS2Service_GetParam_RoutesToOwningRMW verifies a node-targeted command
// runs only in the sidecar whose graph has the node (found via `node list`),
// never in the wrong RMW where it would block on discovery (WDY-1594).
func TestROS2Service_GetParam_RoutesToOwningRMW(t *testing.T) {
	rt := twoSidecarRuntime(func(_ context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
		key := strings.Join(opts.Args, " ")
		switch {
		case key == "node list" && opts.SidecarName == "sc-cyc":
			io.WriteString(stdout, "/other\n") // /talker is NOT here
			return 0, nil
		case key == "node list" && opts.SidecarName == "sc-fast":
			io.WriteString(stdout, "/talker\n")
			return 0, nil
		case strings.HasPrefix(key, "param get") && opts.SidecarName == "sc-fast":
			io.WriteString(stdout, "Boolean value is: True\n")
			return 0, nil
		default:
			fmt.Fprint(stderr, "node not found") // param get in the wrong sidecar
			return 1, nil
		}
	})
	svc := newTestROS2Service(t, rt, t.TempDir())
	got, err := svc.GetParam(context.Background(), &agentpbv2.GetROS2ParamRequest{Node: "/talker", Param: "use_sim_time"})
	if err != nil {
		t.Fatalf("GetParam: %v", err)
	}
	if got.GetValue() != "Boolean value is: True" {
		t.Errorf("value = %q", got.GetValue())
	}
	for _, c := range rt.calls {
		if strings.HasPrefix(strings.Join(c.Args, " "), "param get") && c.SidecarName != "sc-fast" {
			t.Errorf("param get ran in %q; must route to the owning sidecar sc-fast", c.SidecarName)
		}
	}
}

// TestROS2Service_Doctor_SkipsFailedSidecar verifies one sidecar's exec failure
// doesn't hide the others' reports (WDY-1594).
func TestROS2Service_Doctor_SkipsFailedSidecar(t *testing.T) {
	rt := twoSidecarRuntime(func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
		if opts.SidecarName == "sc-cyc" {
			return 0, errors.New("exec failed") // genuine exec error, not a check failure
		}
		io.WriteString(stdout, "FASTRTPS REPORT OK\n")
		return 0, nil
	})
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.Doctor(context.Background(), &agentpbv2.ROS2DoctorRequest{})
	if err != nil {
		t.Fatalf("Doctor should skip the failing sidecar, got error: %v", err)
	}
	if !strings.Contains(resp.GetReport(), "FASTRTPS REPORT OK") {
		t.Errorf("report missing the healthy sidecar's output: %q", resp.GetReport())
	}
}

func TestROS2Service_DomainOverride(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 7},
		outputs: map[string]string{"node list": ""},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	override := int32(42)
	if _, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{DomainId: &override}); err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if rt.calls[0].DomainID != 42 {
		t.Errorf("exec domain = %d, want override 42", rt.calls[0].DomainID)
	}

	bad := int32(233) // first value above the max valid ROS_DOMAIN_ID (232)
	_, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{DomainId: &bad})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("out-of-range override error = %v, want InvalidArgument", err)
	}
}

func TestROS2Service_NoSidecarIsFailedPrecondition(t *testing.T) {
	rt := &fakeROS2Runtime{ensureErr: errors.New("no running ROS 2 containers found")}
	svc := newTestROS2Service(t, rt, t.TempDir())
	_, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("error = %v, want FailedPrecondition", err)
	}
}

func TestROS2Service_ListTopicsWithCounts(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		outputs: map[string]string{
			"topic list -t":    "/scan [sensor_msgs/msg/LaserScan]\n",
			"topic info /scan": "Type: sensor_msgs/msg/LaserScan\nPublisher count: 1\nSubscription count: 3\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.ListTopics(context.Background(), &agentpbv2.ListROS2TopicsRequest{IncludeCounts: true})
	if err != nil {
		t.Fatalf("ListTopics: %v", err)
	}
	if len(resp.GetTopics()) != 1 {
		t.Fatalf("got %d topics", len(resp.GetTopics()))
	}
	topic := resp.GetTopics()[0]
	if topic.GetPublisherCount() != 1 || topic.GetSubscriberCount() != 3 {
		t.Errorf("counts = %d/%d, want 1/3", topic.GetPublisherCount(), topic.GetSubscriberCount())
	}
}

func TestROS2Service_GetSetParam(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		outputs: map[string]string{
			"param get /talker use_sim_time":      "Boolean value is: False\n",
			"param set /talker use_sim_time true": "Set parameter successful\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	got, err := svc.GetParam(context.Background(), &agentpbv2.GetROS2ParamRequest{Node: "/talker", Param: "use_sim_time"})
	if err != nil {
		t.Fatalf("GetParam: %v", err)
	}
	if got.GetValue() != "Boolean value is: False" {
		t.Errorf("value = %q", got.GetValue())
	}
	set, err := svc.SetParam(context.Background(), &agentpbv2.SetROS2ParamRequest{Node: "/talker", Param: "use_sim_time", Value: "true"})
	if err != nil {
		t.Fatalf("SetParam: %v", err)
	}
	if !set.GetSuccess() {
		t.Errorf("SetParam success = false: %s", set.GetMessage())
	}

	// Injection attempts must be rejected before reaching the runtime.
	if _, err := svc.GetParam(context.Background(), &agentpbv2.GetROS2ParamRequest{Node: "/talker; reboot", Param: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("crafted node name error = %v, want InvalidArgument", err)
	}
}

func TestROS2Service_GetGraph(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		outputs: map[string]string{
			"node list": "/lidar\n/slam\n",
			"node info /lidar": `/lidar
  Subscribers:
  Publishers:
    /scan: sensor_msgs/msg/LaserScan
`,
			"node info /slam": `/slam
  Subscribers:
    /scan: sensor_msgs/msg/LaserScan
  Publishers:
    /map: nav_msgs/msg/OccupancyGrid
`,
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetGraph(context.Background(), &agentpbv2.GetROS2GraphRequest{})
	if err != nil {
		t.Fatalf("GetGraph: %v", err)
	}
	if len(resp.GetNodes()) != 2 || len(resp.GetPublishes()) != 2 || len(resp.GetSubscribes()) != 1 {
		t.Errorf("graph = %d nodes, %d pubs, %d subs; want 2/2/1", len(resp.GetNodes()), len(resp.GetPublishes()), len(resp.GetSubscribes()))
	}
	if resp.GetSubscribes()[0].GetNode() != "/slam" || resp.GetSubscribes()[0].GetTopic() != "/scan" {
		t.Errorf("subscribe edge = %+v", resp.GetSubscribes()[0])
	}
}

// fakeServerStream implements grpc.ServerStreamingServer[T] for tests.
type fakeServerStream[T any] struct {
	grpc.ServerStream
	ctx  context.Context
	sent []*T
}

func (f *fakeServerStream[T]) Send(msg *T) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeServerStream[T]) Context() context.Context { return f.ctx }

func TestROS2Service_EchoTopic_CountLimit(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			for i := 0; ; i++ {
				if ctx.Err() != nil {
					return 130, ctx.Err()
				}
				if i > 100 {
					return 0, nil // safety: fake publisher gives up eventually
				}
				fmt.Fprintf(stdout, "data: msg-%d\n---\n", i)
			}
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2Message]{ctx: context.Background()}
	err := svc.EchoTopic(&agentpbv2.EchoROS2TopicRequest{Topic: "/chatter", Count: 3}, stream)
	if err != nil {
		t.Fatalf("EchoTopic: %v", err)
	}
	if len(stream.sent) != 3 {
		t.Fatalf("got %d messages, want 3", len(stream.sent))
	}
	if !strings.Contains(stream.sent[0].GetYaml(), "msg-0") {
		t.Errorf("first message = %q", stream.sent[0].GetYaml())
	}
}

// TestROS2Service_EchoTopic_CountLimit_UnblocksBlockedPublisher guards WDY-1698:
// once the count limit is reached, EchoTopic must return promptly even if the
// publisher goroutine is blocked writing into the pipe (it must not wedge on
// <-execDone). The fake blocks on every write until ctx is cancelled.
func TestROS2Service_EchoTopic_CountLimit_UnblocksBlockedPublisher(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			// Routing calls (topic list) must return quickly so EchoTopic reaches
			// the echo loop. Only the topic echo call should block mid-write.
			if len(opts.Args) > 0 && opts.Args[0] != "topic" || len(opts.Args) < 2 || opts.Args[1] != "echo" {
				return 0, nil
			}
			// Emit enough docs to satisfy the count, then keep trying to write.
			// Each write may block until the handler stops reading; the fake must
			// observe ctx cancellation and return rather than spin or wedge.
			for i := 0; ; i++ {
				if ctx.Err() != nil {
					return 130, ctx.Err()
				}
				if _, werr := fmt.Fprintf(stdout, "data: msg-%d\n---\n", i); werr != nil {
					// Pipe closed by the handler after the count limit: stop.
					return 130, ctx.Err()
				}
			}
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2Message]{ctx: context.Background()}

	done := make(chan error, 1)
	go func() {
		done <- svc.EchoTopic(&agentpbv2.EchoROS2TopicRequest{Topic: "/chatter", Count: 3}, stream)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EchoTopic: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("EchoTopic did not return after count limit — WDY-1698 deadlock")
	}
	if len(stream.sent) != 3 {
		t.Fatalf("got %d messages, want 3", len(stream.sent))
	}
}

func TestROS2Service_MonitorHz(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			io.WriteString(stdout, "average rate: 30.001\n")
			io.WriteString(stdout, "\tmin: 0.032s max: 0.034s std dev: 0.00041s window: 31\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2HzSample]{ctx: context.Background()}
	if err := svc.MonitorHz(&agentpbv2.MonitorROS2HzRequest{Topic: "/scan"}, stream); err != nil {
		t.Fatalf("MonitorHz: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d samples, want 1", len(stream.sent))
	}
	if stream.sent[0].GetHz() != 30.001 || stream.sent[0].GetWindow() != 31 {
		t.Errorf("sample = %+v", stream.sent[0])
	}
}

func TestROS2Service_ListBags(t *testing.T) {
	bagDir := t.TempDir()
	bagPath := filepath.Join(bagDir, "test-bag")
	if err := os.MkdirAll(bagPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bagPath, "data.db3"), bytes.Repeat([]byte("x"), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	metadata := "rosbag2_bagfile_information:\n  duration:\n    nanoseconds: 2500000000\n"
	if err := os.WriteFile(filepath.Join(bagPath, "metadata.yaml"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := newTestROS2Service(t, &fakeROS2Runtime{}, bagDir)
	resp, err := svc.ListBags(context.Background(), &agentpbv2.ListROS2BagsRequest{})
	if err != nil {
		t.Fatalf("ListBags: %v", err)
	}
	if len(resp.GetBags()) != 1 {
		t.Fatalf("got %d bags, want 1", len(resp.GetBags()))
	}
	bag := resp.GetBags()[0]
	if bag.GetName() != "test-bag" || bag.GetSizeBytes() < 1000 || bag.GetDurationSeconds() != 2.5 {
		t.Errorf("bag = %+v", bag)
	}
}

func TestROS2Service_ListBags_MissingDirIsEmpty(t *testing.T) {
	svc := newTestROS2Service(t, &fakeROS2Runtime{}, filepath.Join(t.TempDir(), "nonexistent"))
	resp, err := svc.ListBags(context.Background(), &agentpbv2.ListROS2BagsRequest{})
	if err != nil || len(resp.GetBags()) != 0 {
		t.Errorf("got (%v, %v), want empty response", resp, err)
	}
}

func TestROS2Service_DownloadBag(t *testing.T) {
	bagDir := t.TempDir()
	bagPath := filepath.Join(bagDir, "dl-bag")
	if err := os.MkdirAll(bagPath, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello rosbag")
	if err := os.WriteFile(filepath.Join(bagPath, "metadata.yaml"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	svc := newTestROS2Service(t, &fakeROS2Runtime{}, bagDir)
	stream := &fakeServerStream[agentpbv2.ROS2BagChunk]{ctx: context.Background()}
	if err := svc.DownloadBag(&agentpbv2.DownloadROS2BagRequest{Name: "dl-bag"}, stream); err != nil {
		t.Fatalf("DownloadBag: %v", err)
	}

	var archive bytes.Buffer
	for _, chunk := range stream.sent {
		archive.Write(chunk.GetData())
	}
	tr := tar.NewReader(&archive)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		names = append(names, hdr.Name)
		if hdr.Name == "dl-bag/metadata.yaml" {
			data, _ := io.ReadAll(tr)
			if !bytes.Equal(data, content) {
				t.Errorf("metadata content = %q", data)
			}
		}
	}
	if len(names) != 2 { // dir entry + file
		t.Errorf("archive entries = %v, want bag dir + metadata.yaml", names)
	}

	// Path traversal and unknown names must be rejected.
	if err := svc.DownloadBag(&agentpbv2.DownloadROS2BagRequest{Name: "../etc"}, stream); status.Code(err) != codes.InvalidArgument {
		t.Errorf("traversal error = %v, want InvalidArgument", err)
	}
	if err := svc.DownloadBag(&agentpbv2.DownloadROS2BagRequest{Name: "missing"}, stream); status.Code(err) != codes.NotFound {
		t.Errorf("missing bag error = %v, want NotFound", err)
	}
}

func TestROS2Service_Exec(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			io.WriteString(stdout, "out-line\n")
			io.WriteString(stderr, "err-line\n")
			return 3, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2ExecOutput]{ctx: context.Background()}
	if err := svc.Exec(&agentpbv2.ROS2ExecRequest{Args: []string{"topic", "bw", "/scan"}}, stream); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var stdout, stderr string
	var exitCode *int32
	for _, msg := range stream.sent {
		stdout += string(msg.GetStdout())
		stderr += string(msg.GetStderr())
		if msg.ExitCode != nil {
			exitCode = msg.ExitCode
		}
	}
	if stdout != "out-line\n" || stderr != "err-line\n" {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
	if exitCode == nil || *exitCode != 3 {
		t.Errorf("exit code = %v, want 3", exitCode)
	}

	if err := svc.Exec(&agentpbv2.ROS2ExecRequest{}, stream); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty args error = %v, want InvalidArgument", err)
	}
}

// fakeBidiStream implements grpc.BidiStreamingServer for RecordBag tests.
type fakeBidiStream[Req, Resp any] struct {
	grpc.ServerStream
	ctx  context.Context
	recv chan *Req
	sent []*Resp
}

func (f *fakeBidiStream[Req, Resp]) Recv() (*Req, error) {
	msg, ok := <-f.recv
	if !ok {
		return nil, io.EOF
	}
	return msg, nil
}

func (f *fakeBidiStream[Req, Resp]) Send(msg *Resp) error {
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeBidiStream[Req, Resp]) Context() context.Context { return f.ctx }

func TestROS2Service_RecordBag_AnchorLossDiagnosis(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar:   ROS2Sidecar{Distro: "humble"},
		verifyErr: errors.New("anchor task gone"),
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			io.WriteString(stdout, "[INFO] [rosbag2_recorder]: Recording...\n")
			return 137, nil // killed: app redeploy tore down the namespace
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeBidiStream[agentpbv2.RecordROS2BagRequest, agentpbv2.RecordROS2BagResponse]{
		ctx:  context.Background(),
		recv: make(chan *agentpbv2.RecordROS2BagRequest, 1),
	}
	stream.recv <- &agentpbv2.RecordROS2BagRequest{
		Command: &agentpbv2.RecordROS2BagRequest_Start{
			Start: &agentpbv2.RecordROS2BagRequest_RecordStart{OutputName: "test-bag"},
		},
	}

	if err := svc.RecordBag(stream); err != nil {
		t.Fatalf("RecordBag: %v", err)
	}
	if len(stream.sent) != 2 {
		t.Fatalf("got %d responses, want RECORDING + ERROR: %+v", len(stream.sent), stream.sent)
	}
	if stream.sent[0].GetState() != agentpbv2.RecordROS2BagResponse_STATE_RECORDING {
		t.Errorf("first response state = %v, want RECORDING", stream.sent[0].GetState())
	}
	errResp := stream.sent[1]
	if errResp.GetState() != agentpbv2.RecordROS2BagResponse_STATE_ERROR {
		t.Errorf("second response state = %v, want ERROR", errResp.GetState())
	}
	msg := errResp.GetMessage()
	if !strings.Contains(msg, "stopped or redeployed while recording") {
		t.Errorf("message should diagnose anchor loss, got: %s", msg)
	}
	if !strings.Contains(msg, "Recording...") {
		t.Errorf("message should include recorder output tail, got: %s", msg)
	}
}

func TestROS2Service_RecordBag_UnexpectedExitWithHealthyAnchor(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(ctx context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			io.WriteString(stderr, "error: storage full\n")
			return 1, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeBidiStream[agentpbv2.RecordROS2BagRequest, agentpbv2.RecordROS2BagResponse]{
		ctx:  context.Background(),
		recv: make(chan *agentpbv2.RecordROS2BagRequest, 1),
	}
	stream.recv <- &agentpbv2.RecordROS2BagRequest{
		Command: &agentpbv2.RecordROS2BagRequest_Start{
			Start: &agentpbv2.RecordROS2BagRequest_RecordStart{},
		},
	}

	if err := svc.RecordBag(stream); err != nil {
		t.Fatalf("RecordBag: %v", err)
	}
	last := stream.sent[len(stream.sent)-1]
	if last.GetState() != agentpbv2.RecordROS2BagResponse_STATE_ERROR {
		t.Fatalf("state = %v, want ERROR", last.GetState())
	}
	if !strings.Contains(last.GetMessage(), "exit code 1") || !strings.Contains(last.GetMessage(), "storage full") {
		t.Errorf("message should include exit code and output, got: %s", last.GetMessage())
	}
}

// TestROS2Service_ListNodes_DedupsAcrossSidecars verifies that a node visible to
// more than one sidecar appears only once per (namespace+name, rmw) pair
// (WDY-1710). Two sidecars each on their own RMW both emit /talker → 2 nodes
// kept (distinct RMWs). A sidecar that emits /talker twice in its stdout → only
// 1 node kept (true duplicate).
func TestROS2Service_ListNodes_DedupsAcrossSidecars(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecars: []ROS2Sidecar{
			{Name: "sc-cyc", Distro: "humble", DomainID: 42, RMW: "rmw_cyclonedds_cpp"},
			{Name: "sc-fast", Distro: "humble", DomainID: 42, RMW: "rmw_fastrtps_cpp"},
		},
		// Both sidecars emit /talker (different RMWs → 2 distinct entries kept).
		// sc-cyc also emits /talker a second time via a duplicate stdout line to
		// prove within-sidecar dedup (same name+rmw → collapsed to 1).
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			switch opts.SidecarName {
			case "sc-cyc":
				// Duplicate line simulates a sidecar reporting the same node twice.
				io.WriteString(stdout, "/talker\n/talker\n")
			case "sc-fast":
				io.WriteString(stdout, "/talker\n")
			}
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.ListNodes(context.Background(), &agentpbv2.ListROS2NodesRequest{})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	// /talker on rmw_cyclonedds_cpp and /talker on rmw_fastrtps_cpp are distinct
	// (different RMW) → 2 entries. The duplicate /talker from sc-cyc is collapsed.
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("got %d nodes, want 2 distinct (name,rmw) pairs", len(resp.GetNodes()))
	}
	// Verify both RMWs are represented.
	rmws := map[string]bool{}
	for _, n := range resp.GetNodes() {
		rmws[n.GetRmw()] = true
	}
	if !rmws["rmw_cyclonedds_cpp"] || !rmws["rmw_fastrtps_cpp"] {
		t.Errorf("RMW coverage = %v, want both cyclonedds and fastrtps", rmws)
	}
}

// TestROS2Service_GetGraph_DedupsEdges verifies that a node's publish/subscribe
// edges are deduplicated when a sidecar's node info stdout contains the same
// topic twice (same (node, topic, rmw) → collapsed to 1 edge) (WDY-1710).
func TestROS2Service_GetGraph_DedupsEdges(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Name: "sc-cyc", Distro: "humble", DomainID: 0, RMW: "rmw_cyclonedds_cpp"},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			key := strings.Join(opts.Args, " ")
			switch key {
			case "node list":
				io.WriteString(stdout, "/talker\n")
			case "node info /talker":
				// Duplicate publisher entry simulates noisy node info output.
				io.WriteString(stdout, `/talker
  Subscribers:
  Publishers:
    /chatter: std_msgs/msg/String
    /chatter: std_msgs/msg/String
`)
			}
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetGraph(context.Background(), &agentpbv2.GetROS2GraphRequest{})
	if err != nil {
		t.Fatalf("GetGraph: %v", err)
	}
	// The /talker→/chatter publish edge must appear exactly once despite the
	// duplicate line in node info stdout.
	if len(resp.GetPublishes()) != 1 {
		t.Fatalf("got %d publish edges, want 1 (deduped)", len(resp.GetPublishes()))
	}
	if resp.GetPublishes()[0].GetNode() != "/talker" || resp.GetPublishes()[0].GetTopic() != "/chatter" {
		t.Errorf("publish edge = %+v", resp.GetPublishes()[0])
	}
}

func TestROS2Service_EchoTopic_StderrNotInPayload(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble"},
		execFn: func(_ context.Context, _ ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			io.WriteString(stderr, "selected interface \"lo\" is not multicast-capable: disabling multicast\n")
			io.WriteString(stdout, "data: 'Hello World: 1'\n---\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2Message]{ctx: context.Background()}
	if err := svc.EchoTopic(&agentpbv2.EchoROS2TopicRequest{Topic: "/chatter", Count: 1}, stream); err != nil {
		t.Fatalf("EchoTopic: %v", err)
	}
	if len(stream.sent) != 1 {
		t.Fatalf("got %d messages, want 1", len(stream.sent))
	}
	if strings.Contains(stream.sent[0].GetYaml(), "multicast") {
		t.Errorf("stderr leaked into echo payload (WDY-1708): %q", stream.sent[0].GetYaml())
	}
}

func TestTailLines(t *testing.T) {
	in := "a\nb\n\nc\nd\ne\n"
	if got := tailLines(in, 3); got != "c\nd\ne" {
		t.Errorf("tailLines = %q, want last 3 non-empty lines", got)
	}
	if got := tailLines("", 5); got != "" {
		t.Errorf("tailLines empty = %q", got)
	}
}
