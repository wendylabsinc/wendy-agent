package commands

import (
	"fmt"
	"sort"
	"strings"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// ros2HiddenGraphTopics are infrastructure topics every node touches; they
// are noise in a connectivity graph (rqt_graph hides them too).
var ros2HiddenGraphTopics = map[string]bool{
	"/rosout":           true,
	"/parameter_events": true,
}

// graphSpansMultipleRMWs reports whether the graph carries nodes from more than
// one RMW, so node labels are RMW-tagged only on mixed-RMW devices (WDY-1594).
func graphSpansMultipleRMWs(graph *agentpbv2.GetROS2GraphResponse) bool {
	seen := map[string]struct{}{}
	for _, n := range graph.GetNodes() {
		if n.GetRmw() != "" {
			seen[n.GetRmw()] = struct{}{}
		}
	}
	return len(seen) > 1
}

// ros2GraphNodeLabel renders a node label, appending its RMW tag when the graph
// spans multiple RMWs so identically-named nodes in different graphs are
// distinguishable.
func ros2GraphNodeLabel(node, rmw string, tagged bool) string {
	if tagged && rmw != "" {
		return node + " [" + ros2RMWShort(rmw) + "]"
	}
	return node
}

// ros2EdgeKey identifies a pub/sub bucket by both topic and RMW so that
// publishers and subscribers are only paired within the same middleware
// (WDY-1712). When the graph is single-RMW, all edges carry Rmw: "" and
// therefore share the same key — identical to the pre-fix behaviour.
type ros2EdgeKey struct{ topic, rmw string }

// ros2GraphEdges builds (topic,rmw) → publishers and (topic,rmw) → subscribers
// maps from a graph response, skipping hidden infrastructure topics. Node
// labels carry an RMW tag when the graph spans multiple RMWs.
func ros2GraphEdges(graph *agentpbv2.GetROS2GraphResponse) (pubs, subs map[ros2EdgeKey][]string) {
	tagged := graphSpansMultipleRMWs(graph)
	pubs = make(map[ros2EdgeKey][]string)
	subs = make(map[ros2EdgeKey][]string)
	for _, e := range graph.GetPublishes() {
		if !ros2HiddenGraphTopics[e.GetTopic()] {
			k := ros2EdgeKey{e.GetTopic(), e.GetRmw()}
			pubs[k] = append(pubs[k], ros2GraphNodeLabel(e.GetNode(), e.GetRmw(), tagged))
		}
	}
	for _, e := range graph.GetSubscribes() {
		if !ros2HiddenGraphTopics[e.GetTopic()] {
			k := ros2EdgeKey{e.GetTopic(), e.GetRmw()}
			subs[k] = append(subs[k], ros2GraphNodeLabel(e.GetNode(), e.GetRmw(), tagged))
		}
	}
	return pubs, subs
}

// renderROS2GraphASCII renders the node graph as one "publisher ──topic──▶
// subscriber" line per edge, with dangling publications and isolated nodes
// listed afterwards (WDY-1333).
func renderROS2GraphASCII(graph *agentpbv2.GetROS2GraphResponse) string {
	pubs, subs := ros2GraphEdges(graph)

	keys := make([]ros2EdgeKey, 0, len(pubs))
	for k := range pubs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].topic != keys[j].topic {
			return keys[i].topic < keys[j].topic
		}
		return keys[i].rmw < keys[j].rmw
	})

	var b strings.Builder
	connected := make(map[string]bool)
	for _, k := range keys {
		for _, pub := range pubs[k] {
			if len(subs[k]) == 0 {
				fmt.Fprintf(&b, "[%s] ──%s──▶ (no subscribers)\n", pub, k.topic)
				connected[pub] = true
				continue
			}
			for _, sub := range subs[k] {
				fmt.Fprintf(&b, "[%s] ──%s──▶ [%s]\n", pub, k.topic, sub)
				connected[pub] = true
				connected[sub] = true
			}
		}
	}

	tagged := graphSpansMultipleRMWs(graph)
	var isolated []string
	for _, node := range graph.GetNodes() {
		label := ros2GraphNodeLabel(ros2GraphNodeFQN(node), node.GetRmw(), tagged)
		if !connected[label] {
			isolated = append(isolated, label)
		}
	}
	sort.Strings(isolated)
	if len(isolated) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Isolated nodes (no graph connections):\n")
		for _, node := range isolated {
			fmt.Fprintf(&b, "  [%s]\n", node)
		}
	}
	if b.Len() == 0 {
		return "No ROS 2 nodes found.\n"
	}
	return b.String()
}

// renderROS2GraphDOT renders the node graph as Graphviz DOT, with nodes as
// boxes and topics as edge labels.
func renderROS2GraphDOT(graph *agentpbv2.GetROS2GraphResponse) string {
	pubs, subs := ros2GraphEdges(graph)

	var b strings.Builder
	b.WriteString("digraph ros2 {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box];\n")
	tagged := graphSpansMultipleRMWs(graph)
	for _, node := range graph.GetNodes() {
		fmt.Fprintf(&b, "  %q;\n", ros2GraphNodeLabel(ros2GraphNodeFQN(node), node.GetRmw(), tagged))
	}
	keys := make([]ros2EdgeKey, 0, len(pubs))
	for k := range pubs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].topic != keys[j].topic {
			return keys[i].topic < keys[j].topic
		}
		return keys[i].rmw < keys[j].rmw
	})
	for _, k := range keys {
		for _, pub := range pubs[k] {
			for _, sub := range subs[k] {
				fmt.Fprintf(&b, "  %q -> %q [label=%q];\n", pub, sub, k.topic)
			}
		}
	}
	b.WriteString("}\n")
	return b.String()
}

// ros2GraphNodeFQN joins a node's namespace and name into its
// fully-qualified graph name.
func ros2GraphNodeFQN(n *agentpbv2.ROS2Node) string {
	ns := n.GetNamespace()
	if ns == "" || ns == "/" {
		return "/" + n.GetName()
	}
	return ns + "/" + n.GetName()
}
