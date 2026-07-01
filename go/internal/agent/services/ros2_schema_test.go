package services

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestROS2OwnFields(t *testing.T) {
	show := "std_msgs/Header header\n" +
		"\tbuiltin_interfaces/Time stamp\n" +
		"\t\tint32 sec\n" +
		"\t\tuint32 nanosec\n" +
		"\tstring frame_id\n" +
		"uint32 height\n" +
		"uint8[] data\n"
	got := ros2OwnFields(show)
	want := []string{"std_msgs/Header header", "uint32 height", "uint8[] data"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ros2OwnFields = %q, want %q", got, want)
	}
}

func TestROS2OwnFields_DropsCommentsAndBlanks(t *testing.T) {
	show := "# a comment\n\nuint8 STATUS_OK=0\nint32 value\n\t# nested comment\n\tint32 nested\n"
	got := ros2OwnFields(show)
	want := []string{"uint8 STATUS_OK=0", "int32 value"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ros2OwnFields = %q, want %q", got, want)
	}
}

func TestROS2ComplexTypesIn(t *testing.T) {
	fields := []string{"std_msgs/Header header", "uint32 height", "geometry_msgs/Point[] pts", "string name", "uint8[36] cov", "some_msgs/Foo<=10 foos"}
	got := ros2ComplexTypesIn(fields)
	want := []string{"std_msgs/Header", "geometry_msgs/Point", "some_msgs/Foo"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ros2ComplexTypesIn = %v, want %v", got, want)
	}
}

func TestNormalizeMsgType(t *testing.T) {
	cases := map[string]string{
		"std_msgs/Header":     "std_msgs/msg/Header",
		"std_msgs/msg/Header": "std_msgs/msg/Header",
		"geometry_msgs/Point": "geometry_msgs/msg/Point",
	}
	for in, want := range cases {
		if got := normalizeMsgType(in); got != want {
			t.Errorf("normalizeMsgType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAssembleROS2MsgSchema(t *testing.T) {
	root := "std_msgs/Header header\nuint32 height"
	deps := map[string]string{
		"std_msgs/Header":         "builtin_interfaces/Time stamp\nstring frame_id",
		"builtin_interfaces/Time": "int32 sec\nuint32 nanosec",
	}
	order := []string{"std_msgs/Header", "builtin_interfaces/Time"}
	got := assembleROS2MsgSchema(root, deps, order)
	sep := strings.Repeat("=", 80)
	want := root + "\n" + sep + "\nMSG: std_msgs/Header\nbuiltin_interfaces/Time stamp\nstring frame_id\n" +
		sep + "\nMSG: builtin_interfaces/Time\nint32 sec\nuint32 nanosec"
	if got != want {
		t.Fatalf("assembleROS2MsgSchema =\n%q\nwant\n%q", got, want)
	}

	// A dependency body carrying a trailing newline must not introduce a blank
	// line before the next "====" separator (or at end of output).
	depsNL := map[string]string{
		"std_msgs/Header":         "builtin_interfaces/Time stamp\nstring frame_id\n",
		"builtin_interfaces/Time": "int32 sec\nuint32 nanosec\n",
	}
	if got := assembleROS2MsgSchema(root, depsNL, order); got != want {
		t.Fatalf("assembleROS2MsgSchema (trailing newline) =\n%q\nwant\n%q", got, want)
	}
}

func TestGetMessageDefinition(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		outputs: map[string]string{
			"topic list":                                 "/image\n",
			"topic type /image":                          "sensor_msgs/msg/Image\n",
			"interface show sensor_msgs/msg/Image":       "std_msgs/Header header\n\tbuiltin_interfaces/Time stamp\n\t\tint32 sec\n\tstring frame_id\nuint32 height\n",
			"interface show std_msgs/msg/Header":         "builtin_interfaces/Time stamp\n\tint32 sec\nstring frame_id\n",
			"interface show builtin_interfaces/msg/Time": "int32 sec\nuint32 nanosec\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetMessageDefinition(context.Background(), &agentpbv2.GetROS2MessageDefinitionRequest{Topic: "/image"})
	if err != nil {
		t.Fatalf("GetMessageDefinition: %v", err)
	}
	if resp.GetMessageType() != "sensor_msgs/msg/Image" {
		t.Fatalf("type = %q", resp.GetMessageType())
	}
	if !strings.Contains(resp.GetSchema(), "MSG: std_msgs/Header") ||
		!strings.Contains(resp.GetSchema(), "MSG: builtin_interfaces/Time") {
		t.Fatalf("schema missing deps:\n%s", resp.GetSchema())
	}
	if !strings.HasPrefix(resp.GetSchema(), "std_msgs/Header header\nuint32 height") {
		t.Fatalf("root body wrong:\n%s", resp.GetSchema())
	}
}

// TestGetMessageDefinition_DependencyCap verifies the dependency walk is bounded:
// a type graph that chains to a fresh distinct type forever must fail with
// codes.ResourceExhausted instead of spawning unbounded `ros2 interface show`
// execs (Finding 1 — DoS via deep/wide type graphs).
func TestGetMessageDefinition_DependencyCap(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			args := strings.Join(opts.Args, " ")
			switch {
			case args == "topic list":
				io.WriteString(stdout, "/deep\n")
			case args == "topic type /deep":
				io.WriteString(stdout, "pkg/msg/T0\n")
			case strings.HasPrefix(args, "interface show pkg/msg/T"):
				// Each type references exactly one fresh, never-before-seen type, so
				// the graph is an infinite chain T0 -> T1 -> T2 -> ...
				n, _ := strconv.Atoi(strings.TrimPrefix(args, "interface show pkg/msg/T"))
				fmt.Fprintf(stdout, "pkg/T%d child\n", n+1)
			default:
				return 1, nil
			}
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	_, err := svc.GetMessageDefinition(context.Background(), &agentpbv2.GetROS2MessageDefinitionRequest{Topic: "/deep"})
	if err == nil {
		t.Fatal("GetMessageDefinition: expected error on unbounded type graph, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("status code = %v, want ResourceExhausted", status.Code(err))
	}
	// The walk must stop near the cap, not run away: count the interface-show execs.
	shows := 0
	for _, c := range rt.calls {
		if len(c.Args) >= 2 && c.Args[0] == "interface" && c.Args[1] == "show" {
			shows++
		}
	}
	if shows > ros2MaxMsgDeps+1 {
		t.Fatalf("interface show called %d times, want <= %d (cap not enforced)", shows, ros2MaxMsgDeps+1)
	}
}

// rawCollector implements grpc.ServerStreamingServer[agentpbv2.RawROS2Message].
type rawCollector struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*agentpbv2.RawROS2Message
}

func (c *rawCollector) Context() context.Context { return c.ctx }
func (c *rawCollector) Send(m *agentpbv2.RawROS2Message) error {
	// The handler reuses the frame buffer across sends, so copy the payload the
	// way gRPC's synchronous marshal would, to make assertions stable.
	c.msgs = append(c.msgs, &agentpbv2.RawROS2Message{
		Cdr:         append([]byte(nil), m.GetCdr()...),
		TimestampNs: m.GetTimestampNs(),
	})
	return nil
}

// isForwarder reports whether an exec invocation is the rclpy CDR forwarder
// (python3 -c <script> <topic>) rather than a routing `ros2 topic list`.
func isForwarder(opts ROS2ExecOptions) bool {
	return opts.Binary == "python3"
}

func TestGetServiceDefinition(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		outputs: map[string]string{
			"service list":                        "/set_bool\n",
			"service type /set_bool":              "std_srvs/srv/SetBool\n",
			"interface show std_srvs/srv/SetBool": "bool data\n---\nbool success\nstring message\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetServiceDefinition(context.Background(), &agentpbv2.GetROS2ServiceDefinitionRequest{Service: "/set_bool"})
	if err != nil {
		t.Fatalf("GetServiceDefinition: %v", err)
	}
	if resp.GetType() != "std_srvs/srv/SetBool" {
		t.Fatalf("type = %q", resp.GetType())
	}
	if strings.TrimSpace(resp.GetRequestSchema()) != "bool data" {
		t.Fatalf("request schema = %q, want \"bool data\"", resp.GetRequestSchema())
	}
	if !strings.Contains(resp.GetResponseSchema(), "bool success") || !strings.Contains(resp.GetResponseSchema(), "string message") {
		t.Fatalf("response schema = %q", resp.GetResponseSchema())
	}
}

func TestPublish(t *testing.T) {
	var gotArgs []string
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			if strings.Join(opts.Args, " ") == "topic list" {
				io.WriteString(stdout, "/cmd_vel\n")
				return 0, nil
			}
			gotArgs = opts.Args
			io.WriteString(stdout, "publisher: beginning loop\npublishing #1\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.Publish(context.Background(), &agentpbv2.PublishROS2Request{
		Topic: "/cmd_vel", Type: "geometry_msgs/msg/Twist", Yaml: "{linear: {x: 1.0}}",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("Publish not success: %s", resp.GetMessage())
	}
	want := []string{"topic", "pub", "--once", "/cmd_vel", "geometry_msgs/msg/Twist", "{linear: {x: 1.0}}"}
	if strings.Join(gotArgs, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("pub args = %v, want %v", gotArgs, want)
	}
}

func TestPublish_RejectsBadTopic(t *testing.T) {
	rt := &fakeROS2Runtime{sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0}}
	svc := newTestROS2Service(t, rt, t.TempDir())
	_, err := svc.Publish(context.Background(), &agentpbv2.PublishROS2Request{
		Topic: "/bad topic; rm -rf", Type: "std_msgs/msg/String", Yaml: "{}",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for a topic with shell metacharacters, got %v", err)
	}
}

func TestSubscribeRaw(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			if !isForwarder(opts) { // routing: pickSidecarForTopic runs `topic list`
				io.WriteString(stdout, "/chatter\n")
				return 0, nil
			}
			// The forwarder writes length-framed binary CDR frames.
			stdout.Write(cdrFrame([]byte{0x00, 0x01}))
			stdout.Write(cdrFrame([]byte{0x02, 0x03}))
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	col := &rawCollector{ctx: context.Background()}
	if err := svc.SubscribeRaw(&agentpbv2.SubscribeRawROS2Request{Topic: "/chatter"}, col); err != nil {
		t.Fatalf("SubscribeRaw: %v", err)
	}
	if len(col.msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(col.msgs))
	}
	if string(col.msgs[0].GetCdr()) != "\x00\x01" || string(col.msgs[1].GetCdr()) != "\x02\x03" {
		t.Fatalf("payloads wrong: %v %v", col.msgs[0].GetCdr(), col.msgs[1].GetCdr())
	}
	if col.msgs[0].GetTimestampNs() == 0 || col.msgs[1].GetTimestampNs() == 0 {
		t.Errorf("expected non-zero timestamps, got %d and %d", col.msgs[0].GetTimestampNs(), col.msgs[1].GetTimestampNs())
	}
	// The forwarder must be invoked as `python3 -c <script> <topic>`.
	var fwd *ROS2ExecOptions
	for i := range rt.calls {
		if isForwarder(rt.calls[i]) {
			fwd = &rt.calls[i]
		}
	}
	if fwd == nil {
		t.Fatal("forwarder exec (python3) was never invoked")
	}
	if len(fwd.Args) != 3 || fwd.Args[0] != "-c" || fwd.Args[2] != "/chatter" {
		t.Fatalf("forwarder args = %v, want [-c <script> /chatter]", fwd.Args)
	}
}

// TestSubscribeRaw_LargeFrame proves the whole point of the forwarder: a single
// multi-megabyte message (e.g. an uncompressed camera frame) flows end-to-end
// intact — the old text path capped this at 16 MiB and inflated it ~4x.
func TestSubscribeRaw_LargeFrame(t *testing.T) {
	big := make([]byte, 5*1024*1024)
	for i := range big {
		big[i] = byte(i)
	}
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			if !isForwarder(opts) {
				io.WriteString(stdout, "/image\n")
				return 0, nil
			}
			stdout.Write(cdrFrame(big))
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	col := &rawCollector{ctx: context.Background()}
	if err := svc.SubscribeRaw(&agentpbv2.SubscribeRawROS2Request{Topic: "/image"}, col); err != nil {
		t.Fatalf("SubscribeRaw: %v", err)
	}
	if len(col.msgs) != 1 || len(col.msgs[0].GetCdr()) != len(big) {
		t.Fatalf("got %d messages (first %d bytes), want 1 of %d bytes", len(col.msgs), len(col.msgs[0].GetCdr()), len(big))
	}
}

// TestSubscribeRaw_ForwarderFailsLoudly verifies that when the forwarder exits
// non-zero having produced zero frames (e.g. rclpy raw subscription unavailable
// on the distro), the handler fails loudly (codes.Internal) with the forwarder's
// stderr surfaced, instead of advertising a silently-empty channel.
func TestSubscribeRaw_ForwarderFailsLoudly(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, stderr io.Writer) (int, error) {
			if !isForwarder(opts) {
				io.WriteString(stdout, "/chatter\n")
				return 0, nil
			}
			io.WriteString(stderr, "wendy-forward: could not resolve message type")
			return 2, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	col := &rawCollector{ctx: context.Background()}
	err := svc.SubscribeRaw(&agentpbv2.SubscribeRawROS2Request{Topic: "/chatter"}, col)
	if err == nil {
		t.Fatal("SubscribeRaw: expected error, got nil")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("status code = %v, want Internal", status.Code(err))
	}
	if !strings.Contains(err.Error(), "could not resolve message type") {
		t.Fatalf("error should surface forwarder stderr, got: %v", err)
	}
	if len(col.msgs) != 0 {
		t.Fatalf("got %d messages, want 0", len(col.msgs))
	}
}
