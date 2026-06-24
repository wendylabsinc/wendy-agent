package services

import (
	"context"
	"strings"
	"testing"

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
	fields := []string{"std_msgs/Header header", "uint32 height", "geometry_msgs/Point[] pts", "string name", "uint8[36] cov"}
	got := ros2ComplexTypesIn(fields)
	want := []string{"std_msgs/Header", "geometry_msgs/Point"}
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
}

func TestGetMessageDefinition(t *testing.T) {
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Distro: "humble", DomainID: 0},
		outputs: map[string]string{
			"topic list":                              "/image\n",
			"topic type /image":                       "sensor_msgs/msg/Image\n",
			"interface show sensor_msgs/msg/Image":    "std_msgs/Header header\n\tbuiltin_interfaces/Time stamp\n\t\tint32 sec\n\tstring frame_id\nuint32 height\n",
			"interface show std_msgs/msg/Header":      "builtin_interfaces/Time stamp\n\tint32 sec\nstring frame_id\n",
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
