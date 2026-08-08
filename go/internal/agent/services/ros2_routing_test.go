package services

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// Two costs this covers:
//
//  1. GetGraph ran `ros2 node info` once per node, serially. An exec is ~1s
//     (container exec dispatch + sourcing setup.bash + a fresh rclpy process
//     doing DDS discovery), so a 40-node robot took 40s+ for one
//     `wendy device ros2 graph`. ListTopics was already fanned out; this was not.
//  2. pickSidecarOwning ran an extra `<kind> list` exec *per sidecar* on every
//     targeted call, to choose between candidates — including when there was only
//     one candidate, i.e. on essentially every real device.

// countingRuntime answers node list/info and records call counts + concurrency.
type countingRuntime struct {
	sidecars []ROS2Sidecar
	nodes    []string

	mu          sync.Mutex
	calls       []string
	inFlight    int32
	maxInFlight int32
	// delay is applied to `node info` so overlap is observable.
	delay time.Duration
}

func (c *countingRuntime) FindROS2Containers(context.Context) ([]ROS2Target, error) {
	return nil, nil
}
func (c *countingRuntime) EnsureROS2Sidecars(context.Context) ([]ROS2Sidecar, error) {
	return c.sidecars, nil
}
func (c *countingRuntime) StopROS2Sidecar(context.Context) error   { return nil }
func (c *countingRuntime) VerifyROS2Sidecar(context.Context) error { return nil }

func (c *countingRuntime) ExecROS2(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
	joined := strings.Join(opts.Args, " ")
	c.mu.Lock()
	c.calls = append(c.calls, opts.SidecarName+"|"+joined)
	c.mu.Unlock()

	switch {
	case joined == "node list":
		_, _ = io.WriteString(stdout, strings.Join(c.nodes, "\n")+"\n")
		return 0, nil
	case strings.HasPrefix(joined, "node info "):
		n := atomic.AddInt32(&c.inFlight, 1)
		for {
			cur := atomic.LoadInt32(&c.maxInFlight)
			if n <= cur || atomic.CompareAndSwapInt32(&c.maxInFlight, cur, n) {
				break
			}
		}
		if c.delay > 0 {
			time.Sleep(c.delay)
		}
		atomic.AddInt32(&c.inFlight, -1)
		node := strings.TrimPrefix(joined, "node info ")
		fmt.Fprintf(stdout, "%s\n  Subscribers:\n    /in: std_msgs/msg/String\n  Publishers:\n    /out: std_msgs/msg/String\n", node)
		return 0, nil
	}
	return 1, nil
}

func (c *countingRuntime) callsMatching(substr string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, call := range c.calls {
		if strings.Contains(call, substr) {
			n++
		}
	}
	return n
}

func TestROS2Service_GetGraph_NodeInfoRunsConcurrently(t *testing.T) {
	var nodes []string
	for i := 0; i < 24; i++ {
		nodes = append(nodes, fmt.Sprintf("/node_%02d", i))
	}
	rt := &countingRuntime{
		sidecars: []ROS2Sidecar{{Name: "sc", Distro: "humble", DomainID: 7}},
		nodes:    nodes,
		delay:    40 * time.Millisecond,
	}
	svc := newTestROS2Service(t, rt, t.TempDir())

	start := time.Now()
	resp, err := svc.GetGraph(context.Background(), &agentpbv2.GetROS2GraphRequest{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetGraph: %v", err)
	}

	if got := len(resp.GetNodes()); got != len(nodes) {
		t.Errorf("got %d nodes, want %d", got, len(nodes))
	}
	if rt.maxInFlight < 2 {
		t.Errorf("max concurrent `node info` execs = %d, want > 1 (they ran serially)", rt.maxInFlight)
	}
	if rt.maxInFlight > ros2TopicInfoConcurrency {
		t.Errorf("max concurrent = %d, exceeds the %d bound; this can starve other "+
			"RPCs against the sidecar's exec table", rt.maxInFlight, ros2TopicInfoConcurrency)
	}
	// Serial would be 24*40ms = 960ms. Bounded at 8, expect ~3 waves = ~120ms.
	if elapsed > 700*time.Millisecond {
		t.Errorf("GetGraph took %s for %d nodes; that is serial-shaped", elapsed, len(nodes))
	}
}

func TestROS2Service_GetGraph_EdgesStayInListingOrder(t *testing.T) {
	// Concurrency must not make the response order depend on scheduling.
	nodes := []string{"/aaa", "/bbb", "/ccc", "/ddd", "/eee", "/fff", "/ggg", "/hhh", "/iii"}
	rt := &countingRuntime{
		sidecars: []ROS2Sidecar{{Name: "sc", Distro: "humble", DomainID: 7}},
		nodes:    nodes,
	}
	svc := newTestROS2Service(t, rt, t.TempDir())

	var first []string
	for run := 0; run < 5; run++ {
		resp, err := svc.GetGraph(context.Background(), &agentpbv2.GetROS2GraphRequest{})
		if err != nil {
			t.Fatalf("GetGraph: %v", err)
		}
		var order []string
		for _, e := range resp.GetPublishes() {
			order = append(order, e.GetNode())
		}
		if run == 0 {
			first = order
			if len(order) != len(nodes) {
				t.Fatalf("got %d publish edges, want %d", len(order), len(nodes))
			}
			for i, n := range nodes {
				if order[i] != n {
					t.Fatalf("edge %d = %q, want %q (listing order)", i, order[i], n)
				}
			}
			continue
		}
		if strings.Join(order, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d order %v differs from run 0 %v", run, order, first)
		}
	}
}

func TestROS2Service_SingleSidecarSkipsRoutingExec(t *testing.T) {
	// The routing probe exists to choose between RMW graphs. With one sidecar
	// there is nothing to choose, and the extra `topic list` exec doubled the
	// latency of every targeted call.
	rt := &fakeROS2Runtime{
		sidecar: ROS2Sidecar{Name: "sc", Distro: "humble", DomainID: 7},
		outputs: map[string]string{
			"topic list":               "/chatter\n",
			"param get /talker my_int": "Integer value is: 42\n",
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	if _, err := svc.GetParam(context.Background(), &agentpbv2.GetROS2ParamRequest{
		Node: "/talker", Param: "my_int",
	}); err != nil {
		t.Fatalf("GetParam: %v", err)
	}
	for _, c := range rt.calls {
		if strings.Join(c.Args, " ") == "node list" {
			t.Fatal("GetParam ran a `node list` routing probe with only one sidecar")
		}
	}
	if len(rt.calls) != 1 {
		t.Errorf("made %d execs, want 1 (just the param get)", len(rt.calls))
	}
}

func TestROS2Service_MultiSidecarStillRoutes(t *testing.T) {
	// The short-circuit must not disable routing where it matters.
	rt := twoSidecarRuntime(func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
		joined := strings.Join(opts.Args, " ")
		switch {
		case joined == "node list" && opts.SidecarName == "sc-fast":
			_, _ = io.WriteString(stdout, "/talker\n")
			return 0, nil
		case joined == "node list":
			_, _ = io.WriteString(stdout, "/other\n")
			return 0, nil
		case strings.HasPrefix(joined, "param get "):
			if opts.SidecarName != "sc-fast" {
				return 1, nil
			}
			_, _ = io.WriteString(stdout, "Integer value is: 42\n")
			return 0, nil
		}
		return 1, nil
	})
	svc := newTestROS2Service(t, rt, t.TempDir())
	resp, err := svc.GetParam(context.Background(), &agentpbv2.GetROS2ParamRequest{
		Node: "/talker", Param: "my_int",
	})
	if err != nil {
		t.Fatalf("GetParam: %v", err)
	}
	if !strings.Contains(resp.GetValue(), "42") {
		t.Errorf("value = %q, want the owning sidecar's answer", resp.GetValue())
	}
	sawRouting := false
	for _, c := range rt.calls {
		if strings.Join(c.Args, " ") == "node list" {
			sawRouting = true
		}
	}
	if !sawRouting {
		t.Error("with two sidecars, GetParam must still probe for the owning graph")
	}
}

func TestROS2Service_SidecarOrderIsDeterministic(t *testing.T) {
	// `scs[0]` is the fallback for commands that cannot be RMW-routed (raw Exec,
	// `bag record -a`). It used to inherit containerd's unspecified Containers()
	// order; it is now sorted by sidecar name, so which DDS graph a raw
	// passthrough lands on is a contract rather than an accident.
	unsorted := []ROS2Sidecar{
		{Name: "sc-zulu", Distro: "humble", DomainID: 1, RMW: "rmw_gurumdds_cpp"},
		{Name: "sc-alpha", Distro: "humble", DomainID: 2, RMW: "rmw_cyclonedds_cpp"},
		{Name: "sc-mike", Distro: "humble", DomainID: 3, RMW: "rmw_fastrtps_cpp"},
	}
	for run := 0; run < 5; run++ {
		rt := &fakeROS2Runtime{
			sidecars: unsorted,
			outputs:  map[string]string{"doctor --report": "all good\n"},
		}
		svc := newTestROS2Service(t, rt, t.TempDir())
		scs, err := svc.resolveSidecars(context.Background(), nil)
		if err != nil {
			t.Fatalf("resolveSidecars: %v", err)
		}
		want := []string{"sc-alpha", "sc-mike", "sc-zulu"}
		for i, w := range want {
			if scs[i].name != w {
				t.Fatalf("run %d: sidecar %d = %q, want %q", run, i, scs[i].name, w)
			}
		}
	}
}

func TestROS2Service_ExecFallsBackToTheLowestNamedSidecar(t *testing.T) {
	// Raw passthrough can't be routed (opaque args), so it uses scs[0]. Pin which
	// one that is.
	rt := &fakeROS2Runtime{
		sidecars: []ROS2Sidecar{
			{Name: "sc-zulu", Distro: "humble", DomainID: 1},
			{Name: "sc-alpha", Distro: "humble", DomainID: 2},
		},
		execFn: func(_ context.Context, opts ROS2ExecOptions, stdout, _ io.Writer) (int, error) {
			_, _ = io.WriteString(stdout, "ran on "+opts.SidecarName+"\n")
			return 0, nil
		},
	}
	svc := newTestROS2Service(t, rt, t.TempDir())
	stream := &fakeServerStream[agentpbv2.ROS2ExecOutput]{ctx: context.Background()}
	if err := svc.Exec(&agentpbv2.ROS2ExecRequest{Args: []string{"pkg", "list"}}, stream); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(rt.calls) == 0 {
		t.Fatal("no exec recorded")
	}
	if rt.calls[0].SidecarName != "sc-alpha" {
		t.Errorf("raw Exec ran on %q, want the deterministic first sidecar sc-alpha",
			rt.calls[0].SidecarName)
	}
}
