package services

import (
	"reflect"
	"testing"
)

func TestParseROS2NodeList(t *testing.T) {
	out := "/talker\n/camera/driver\n\n/ns/sub/node\n"
	nodes := parseROS2NodeList(out)
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3: %+v", len(nodes), nodes)
	}
	if nodes[0].GetName() != "talker" || nodes[0].GetNamespace() != "/" {
		t.Errorf("nodes[0] = %v/%v, want //talker", nodes[0].GetNamespace(), nodes[0].GetName())
	}
	if nodes[1].GetName() != "driver" || nodes[1].GetNamespace() != "/camera" {
		t.Errorf("nodes[1] = %v %v", nodes[1].GetNamespace(), nodes[1].GetName())
	}
	if got := ros2NodeFQN(nodes[2]); got != "/ns/sub/node" {
		t.Errorf("round-trip FQN = %q, want /ns/sub/node", got)
	}
	if got := ros2NodeFQN(nodes[0]); got != "/talker" {
		t.Errorf("root-namespace FQN = %q, want /talker", got)
	}
}

func TestParseROS2NodeList_IgnoresWarnings(t *testing.T) {
	out := "WARNING: Be aware that nodes can hide\n/talker\n"
	nodes := parseROS2NodeList(out)
	if len(nodes) != 1 || nodes[0].GetName() != "talker" {
		t.Errorf("got %+v, want one node 'talker'", nodes)
	}
}

func TestParseROS2TopicList(t *testing.T) {
	out := "/scan [sensor_msgs/msg/LaserScan]\n/tf [tf2_msgs/msg/TFMessage, geometry_msgs/msg/TransformStamped]\n/bare\n"
	topics := parseROS2TopicList(out)
	if len(topics) != 3 {
		t.Fatalf("got %d topics, want 3", len(topics))
	}
	if topics[0].GetName() != "/scan" || !reflect.DeepEqual(topics[0].GetTypes(), []string{"sensor_msgs/msg/LaserScan"}) {
		t.Errorf("topics[0] = %+v", topics[0])
	}
	if len(topics[1].GetTypes()) != 2 {
		t.Errorf("topics[1].Types = %v, want 2 entries", topics[1].GetTypes())
	}
	if topics[2].GetName() != "/bare" || len(topics[2].GetTypes()) != 0 {
		t.Errorf("topics[2] = %+v", topics[2])
	}
}

func TestParseROS2TopicInfo(t *testing.T) {
	out := "Type: std_msgs/msg/String\nPublisher count: 1\nSubscription count: 2\n"
	types, pubs, subs := parseROS2TopicInfo(out)
	if !reflect.DeepEqual(types, []string{"std_msgs/msg/String"}) || pubs != 1 || subs != 2 {
		t.Errorf("got types=%v pubs=%d subs=%d", types, pubs, subs)
	}
}

func TestParseROS2ParamList_AllNodes(t *testing.T) {
	out := "/camera/driver:\n  exposure\n  gain\n/talker:\n  use_sim_time\n"
	nodes := parseROS2ParamList(out, "")
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(nodes), nodes)
	}
	if nodes[0].GetNode() != "/camera/driver" || !reflect.DeepEqual(nodes[0].GetParams(), []string{"exposure", "gain"}) {
		t.Errorf("nodes[0] = %+v", nodes[0])
	}
	if nodes[1].GetNode() != "/talker" || !reflect.DeepEqual(nodes[1].GetParams(), []string{"use_sim_time"}) {
		t.Errorf("nodes[1] = %+v", nodes[1])
	}
}

func TestParseROS2ParamList_SingleNode(t *testing.T) {
	out := "  exposure\n  gain\n"
	nodes := parseROS2ParamList(out, "/camera/driver")
	if len(nodes) != 1 || nodes[0].GetNode() != "/camera/driver" {
		t.Fatalf("got %+v", nodes)
	}
	if !reflect.DeepEqual(nodes[0].GetParams(), []string{"exposure", "gain"}) {
		t.Errorf("params = %v", nodes[0].GetParams())
	}
}

func TestParseROS2HzBlock(t *testing.T) {
	sample, ok := parseROS2HzBlock("average rate: 10.000", "\tmin: 0.099s max: 0.101s std dev: 0.00050s window: 11")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if sample.GetHz() != 10.0 || sample.GetMinDelta() != 0.099 || sample.GetMaxDelta() != 0.101 ||
		sample.GetStdDev() != 0.0005 || sample.GetWindow() != 11 {
		t.Errorf("sample = %+v", sample)
	}
	if _, ok := parseROS2HzBlock("garbage", "more garbage"); ok {
		t.Error("expected parse failure for garbage input")
	}
}

func TestParseROS2NodeInfo(t *testing.T) {
	out := `/lidar_driver
  Subscribers:
    /parameter_events: rcl_interfaces/msg/ParameterEvent
  Publishers:
    /scan: sensor_msgs/msg/LaserScan
    /rosout: rcl_interfaces/msg/Log
  Service Servers:
    /lidar_driver/get_parameters: rcl_interfaces/srv/GetParameters
`
	pubs, subs := parseROS2NodeInfo(out)
	if !reflect.DeepEqual(pubs, []string{"/scan", "/rosout"}) {
		t.Errorf("publishes = %v", pubs)
	}
	if !reflect.DeepEqual(subs, []string{"/parameter_events"}) {
		t.Errorf("subscribes = %v", subs)
	}
}

func TestParseROS2BagDurationNanos(t *testing.T) {
	metadata := `rosbag2_bagfile_information:
  version: 5
  storage_identifier: sqlite3
  duration:
    nanoseconds: 14694000000
  message_count: 312
`
	nanos, ok := parseROS2BagDurationNanos(metadata)
	if !ok || nanos != 14694000000 {
		t.Errorf("got (%d, %v), want (14694000000, true)", nanos, ok)
	}
	if _, ok := parseROS2BagDurationNanos("no duration here"); ok {
		t.Error("expected failure for metadata without duration")
	}
}

func TestValidateROS2GraphName(t *testing.T) {
	for _, ok := range []string{"/scan", "/camera/image_raw", "~/private", "relative_topic", "/tf2"} {
		if err := validateROS2GraphName(ok); err != nil {
			t.Errorf("validateROS2GraphName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "topic; rm -rf /", "/topic with space", "$(evil)", "-flag", "--unsafe"} {
		if err := validateROS2GraphName(bad); err == nil {
			t.Errorf("validateROS2GraphName(%q) = nil, want error", bad)
		}
	}
}

func TestValidateROS2ParamName(t *testing.T) {
	for _, ok := range []string{"use_sim_time", "robot.wheel.radius", "p1"} {
		if err := validateROS2ParamName(ok); err != nil {
			t.Errorf("validateROS2ParamName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "x;y", "-flag"} {
		if err := validateROS2ParamName(bad); err == nil {
			t.Errorf("validateROS2ParamName(%q) = nil, want error", bad)
		}
	}
}
