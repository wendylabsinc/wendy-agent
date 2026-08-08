package commands

import (
	"strings"
	"testing"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// The graph renderers only walked `pubs`, so a topic with subscribers but no
// publisher produced no line at all — and its subscribers then showed up under
// "Isolated nodes (no graph connections)", which is wrong: they have a
// subscription. It is also the single most useful thing a graph can tell you
// while debugging ("nothing is publishing what my node is waiting for").

func graphWithDanglingSubscription() *agentpbv2.GetROS2GraphResponse {
	return &agentpbv2.GetROS2GraphResponse{
		Nodes: []*agentpbv2.ROS2Node{
			{Name: "listener", Namespace: "/"},
			{Name: "talker", Namespace: "/"},
			{Name: "lonely", Namespace: "/"},
		},
		Publishes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/talker", Topic: "/chatter"},
			{Node: "/talker", Topic: "/unheard"},
		},
		Subscribes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/listener", Topic: "/chatter"},
			// Nothing publishes /commands.
			{Node: "/listener", Topic: "/commands"},
		},
	}
}

func TestRenderROS2GraphASCII_ShowsDanglingSubscription(t *testing.T) {
	out := renderROS2GraphASCII(graphWithDanglingSubscription())

	if !strings.Contains(out, "(no publishers) ──/commands──▶ [/listener]") {
		t.Errorf("dangling subscription not rendered:\n%s", out)
	}
	// The pre-existing mirror case must still work.
	if !strings.Contains(out, "[/talker] ──/unheard──▶ (no subscribers)") {
		t.Errorf("dangling publication not rendered:\n%s", out)
	}
	if !strings.Contains(out, "[/talker] ──/chatter──▶ [/listener]") {
		t.Errorf("connected edge not rendered:\n%s", out)
	}
}

func TestRenderROS2GraphASCII_SubscriberOnlyNodeIsNotIsolated(t *testing.T) {
	graph := &agentpbv2.GetROS2GraphResponse{
		Nodes: []*agentpbv2.ROS2Node{
			{Name: "listener", Namespace: "/"},
			{Name: "lonely", Namespace: "/"},
		},
		Subscribes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/listener", Topic: "/commands"},
		},
	}
	out := renderROS2GraphASCII(graph)

	isolated := out
	if i := strings.Index(out, "Isolated nodes"); i >= 0 {
		isolated = out[i:]
	} else {
		isolated = ""
	}
	if strings.Contains(isolated, "/listener") {
		t.Errorf("/listener has a subscription and must not be listed as isolated:\n%s", out)
	}
	// A node with genuinely no edges still belongs there.
	if !strings.Contains(isolated, "/lonely") {
		t.Errorf("/lonely has no edges and should be listed as isolated:\n%s", out)
	}
}

func TestRenderROS2GraphASCII_HidesInfrastructureTopics(t *testing.T) {
	// /rosout and /parameter_events are noise every node touches; a dangling
	// subscription to them must not resurrect them.
	graph := &agentpbv2.GetROS2GraphResponse{
		Nodes: []*agentpbv2.ROS2Node{{Name: "talker", Namespace: "/"}},
		Subscribes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/talker", Topic: "/parameter_events"},
			{Node: "/talker", Topic: "/rosout"},
		},
	}
	out := renderROS2GraphASCII(graph)
	if strings.Contains(out, "/parameter_events") || strings.Contains(out, "/rosout") {
		t.Errorf("infrastructure topics must stay hidden:\n%s", out)
	}
}

func TestRenderROS2GraphASCII_IsDeterministic(t *testing.T) {
	graph := graphWithDanglingSubscription()
	first := renderROS2GraphASCII(graph)
	for i := 0; i < 8; i++ {
		if got := renderROS2GraphASCII(graph); got != first {
			t.Fatalf("render %d differs:\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

func TestRenderROS2GraphDOT_ShowsDanglingEdgesBothWays(t *testing.T) {
	out := renderROS2GraphDOT(graphWithDanglingSubscription())

	if !strings.Contains(out, `"/commands (no publishers)"`) {
		t.Errorf("DOT should render a placeholder for a dangling subscription:\n%s", out)
	}
	if !strings.Contains(out, `"/unheard (no subscribers)"`) {
		t.Errorf("DOT should render a placeholder for a dangling publication:\n%s", out)
	}
	if !strings.Contains(out, `"/talker" -> "/listener" [label="/chatter"]`) {
		t.Errorf("DOT should render the connected edge:\n%s", out)
	}
	// Well-formed DOT.
	if !strings.HasPrefix(out, "digraph ros2 {\n") || !strings.HasSuffix(out, "}\n") {
		t.Errorf("DOT output is malformed:\n%s", out)
	}
}

func TestRenderROS2GraphDOT_IsDeterministic(t *testing.T) {
	graph := graphWithDanglingSubscription()
	first := renderROS2GraphDOT(graph)
	for i := 0; i < 8; i++ {
		if got := renderROS2GraphDOT(graph); got != first {
			t.Fatalf("DOT render %d differs:\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

func TestRenderROS2GraphASCII_EmptyGraph(t *testing.T) {
	out := renderROS2GraphASCII(&agentpbv2.GetROS2GraphResponse{})
	if out != "No ROS 2 nodes found.\n" {
		t.Errorf("got %q", out)
	}
}

func TestRenderROS2GraphASCII_DanglingSubscriptionAcrossRMWs(t *testing.T) {
	// A publisher on one RMW does not satisfy a subscriber on another (WDY-1712),
	// so both sides must render as dangling and be RMW-tagged.
	graph := &agentpbv2.GetROS2GraphResponse{
		Nodes: []*agentpbv2.ROS2Node{
			{Name: "talker", Namespace: "/", Rmw: "rmw_cyclonedds_cpp"},
			{Name: "listener", Namespace: "/", Rmw: "rmw_fastrtps_cpp"},
		},
		Publishes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/talker", Topic: "/chatter", Rmw: "rmw_cyclonedds_cpp"},
		},
		Subscribes: []*agentpbv2.GetROS2GraphResponse_Edge{
			{Node: "/listener", Topic: "/chatter", Rmw: "rmw_fastrtps_cpp"},
		},
	}
	out := renderROS2GraphASCII(graph)
	if !strings.Contains(out, "(no subscribers)") {
		t.Errorf("the cyclonedds publisher has no same-RMW subscriber:\n%s", out)
	}
	if !strings.Contains(out, "(no publishers)") {
		t.Errorf("the fastrtps subscriber has no same-RMW publisher:\n%s", out)
	}
	if !strings.Contains(out, "cyclonedds") || !strings.Contains(out, "fastrtps") {
		t.Errorf("a mixed-RMW graph must tag node labels:\n%s", out)
	}
}
