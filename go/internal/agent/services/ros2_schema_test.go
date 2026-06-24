package services

import (
	"context"
	"io"
	"strings"
	"testing"

	"google.golang.org/grpc"

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

func TestParsePythonBytesLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{`b'\x00\x01ABC'`, []byte{0x00, 0x01, 'A', 'B', 'C'}},
		{`b'\n\t\\\''`, []byte{'\n', '\t', '\\', '\''}},
		{`b"\x00hi"`, []byte{0x00, 'h', 'i'}},
		{`b''`, []byte{}},
	}
	for _, c := range cases {
		got, err := parsePythonBytesLiteral(c.in)
		if err != nil {
			t.Fatalf("parse(%q): %v", c.in, err)
		}
		if string(got) != string(c.want) {
			t.Errorf("parse(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParsePythonBytesLiteral_Rejects(t *testing.T) {
	for _, in := range []string{"", "notbytes", "b'unterminated", "'noprefix'"} {
		if _, err := parsePythonBytesLiteral(in); err == nil {
			t.Errorf("parse(%q): expected error", in)
		}
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

// rawCollector implements grpc.ServerStreamingServer[agentpbv2.RawROS2Message].
type rawCollector struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*agentpbv2.RawROS2Message
}

func (c *rawCollector) Context() context.Context { return c.ctx }
func (c *rawCollector) Send(m *agentpbv2.RawROS2Message) error {
	c.msgs = append(c.msgs, m)
	return nil
}

func TestSubscribeRaw(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			if strings.Join(opts.Args, " ") == "topic list" {
				io.WriteString(stdout, "/chatter\n")
				return 0, nil
			}
			// topic echo --raw /chatter. An unparseable line is interleaved between
			// two valid messages: it must be warn-and-skipped, not abort the stream.
			io.WriteString(stdout, `b'\x00\x01'`+"\n---\n"+"garbage-not-bytes\n---\n"+`b'\x02\x03'`+"\n---\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	col := &rawCollector{ctx: context.Background()}
	if err := svc.SubscribeRaw(&agentpbv2.SubscribeRawROS2Request{Topic: "/chatter"}, col); err != nil {
		t.Fatalf("SubscribeRaw: %v", err)
	}
	if len(col.msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (garbage line should be skipped)", len(col.msgs))
	}
	if string(col.msgs[0].GetCdr()) != "\x00\x01" || string(col.msgs[1].GetCdr()) != "\x02\x03" {
		t.Fatalf("payloads wrong: %v %v", col.msgs[0].GetCdr(), col.msgs[1].GetCdr())
	}
	if col.msgs[0].GetTimestampNs() == 0 || col.msgs[1].GetTimestampNs() == 0 {
		t.Errorf("expected non-zero timestamps, got %d and %d", col.msgs[0].GetTimestampNs(), col.msgs[1].GetTimestampNs())
	}
}
