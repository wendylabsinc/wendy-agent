package services

import (
	"strconv"
	"strings"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// parseROS2NodeList parses `ros2 node list` output: one fully-qualified node
// name per line (e.g. "/camera/driver").
func parseROS2NodeList(out string) []*agentpbv2.ROS2Node {
	var nodes []*agentpbv2.ROS2Node
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		nodes = append(nodes, ros2NodeFromFQN(line))
	}
	return nodes
}

// ros2NodeFromFQN splits a fully-qualified node name into namespace and name.
// "/camera/driver" → ns "/camera", name "driver"; "/talker" → ns "/", name "talker".
func ros2NodeFromFQN(fqn string) *agentpbv2.ROS2Node {
	idx := strings.LastIndex(fqn, "/")
	ns := fqn[:idx]
	if ns == "" {
		ns = "/"
	}
	return &agentpbv2.ROS2Node{Name: fqn[idx+1:], Namespace: ns}
}

// ros2NodeFQN is the inverse of ros2NodeFromFQN.
func ros2NodeFQN(n *agentpbv2.ROS2Node) string {
	if n.GetNamespace() == "/" || n.GetNamespace() == "" {
		return "/" + n.GetName()
	}
	return n.GetNamespace() + "/" + n.GetName()
}

// parseROS2TopicList parses `ros2 topic list -t` (or `ros2 service list -t`)
// output: lines of the form "/name [pkg/msg/Type]" or "/name [typeA, typeB]".
func parseROS2TopicList(out string) []*agentpbv2.ROS2Topic {
	var topics []*agentpbv2.ROS2Topic
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		name := line
		var types []string
		if open := strings.Index(line, " ["); open >= 0 && strings.HasSuffix(line, "]") {
			name = line[:open]
			for _, t := range strings.Split(line[open+2:len(line)-1], ",") {
				if t = strings.TrimSpace(t); t != "" {
					types = append(types, t)
				}
			}
		}
		topics = append(topics, &agentpbv2.ROS2Topic{Name: name, Types: types})
	}
	return topics
}

// parseROS2TopicInfo extracts type and endpoint counts from `ros2 topic info`
// output ("Type: std_msgs/msg/String", "Publisher count: 1",
// "Subscription count: 2").
func parseROS2TopicInfo(out string) (types []string, pubCount, subCount int32) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Type:"):
			if t := strings.TrimSpace(strings.TrimPrefix(line, "Type:")); t != "" {
				types = append(types, t)
			}
		case strings.HasPrefix(line, "Publisher count:"):
			pubCount = parseROS2Count(line)
		case strings.HasPrefix(line, "Subscription count:"):
			subCount = parseROS2Count(line)
		}
	}
	return types, pubCount, subCount
}

func parseROS2Count(line string) int32 {
	_, val, ok := strings.Cut(line, ":")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0
	}
	return int32(n)
}

// parseROS2ParamList parses `ros2 param list` output. Without a node filter
// the output groups parameters under "  /node_name:" headers; with a filter
// it is a flat indented list. defaultNode is used for flat output.
func parseROS2ParamList(out, defaultNode string) []*agentpbv2.ListROS2ParamsResponse_NodeParams {
	byNode := make(map[string][]string)
	var order []string
	current := defaultNode
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, " \r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		// Node section headers are unindented, start with "/", and end with ":".
		if !strings.HasPrefix(raw, " ") && strings.HasPrefix(trimmed, "/") && strings.HasSuffix(trimmed, ":") {
			current = strings.TrimSuffix(trimmed, ":")
			if _, seen := byNode[current]; !seen {
				byNode[current] = nil
				order = append(order, current)
			}
			continue
		}
		if current == "" {
			continue
		}
		if _, seen := byNode[current]; !seen {
			byNode[current] = nil
			order = append(order, current)
		}
		byNode[current] = append(byNode[current], trimmed)
	}
	var result []*agentpbv2.ListROS2ParamsResponse_NodeParams
	for _, node := range order {
		result = append(result, &agentpbv2.ListROS2ParamsResponse_NodeParams{
			Node:   node,
			Params: byNode[node],
		})
	}
	return result
}

// parseROS2HzBlock parses one `ros2 topic hz` report pair:
//
//	average rate: 10.000
//		min: 0.099s max: 0.101s std dev: 0.00050s window: 11
//
// avgLine is the "average rate:" line; statsLine the following stats line.
func parseROS2HzBlock(avgLine, statsLine string) (*agentpbv2.ROS2HzSample, bool) {
	_, rateStr, ok := strings.Cut(avgLine, "average rate:")
	if !ok {
		return nil, false
	}
	hz, err := strconv.ParseFloat(strings.TrimSpace(rateStr), 64)
	if err != nil {
		return nil, false
	}
	sample := &agentpbv2.ROS2HzSample{Hz: hz}
	fields := strings.Fields(strings.ReplaceAll(statsLine, "std dev:", "stddev:"))
	for i := 0; i+1 < len(fields); i += 2 {
		val := strings.TrimSuffix(fields[i+1], "s")
		switch fields[i] {
		case "min:":
			sample.MinDelta, _ = strconv.ParseFloat(val, 64)
		case "max:":
			sample.MaxDelta, _ = strconv.ParseFloat(val, 64)
		case "stddev:":
			sample.StdDev, _ = strconv.ParseFloat(val, 64)
		case "window:":
			if n, err := strconv.Atoi(val); err == nil {
				sample.Window = int32(n)
			}
		}
	}
	return sample, true
}

// parseROS2NodeInfo extracts the topics a node publishes and subscribes to
// from `ros2 node info <node>` output. Section headers ("Subscribers:",
// "Publishers:", "Service Servers:", …) are unindented relative to entries,
// which have the form "    /topic: pkg/msg/Type".
func parseROS2NodeInfo(out string) (publishes, subscribes []string) {
	section := ""
	for _, raw := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		switch trimmed {
		case "Publishers:":
			section = "pub"
			continue
		case "Subscribers:":
			section = "sub"
			continue
		case "Service Servers:", "Service Clients:", "Action Servers:", "Action Clients:":
			section = ""
			continue
		}
		if section == "" || !strings.HasPrefix(trimmed, "/") {
			continue
		}
		topic, _, _ := strings.Cut(trimmed, ":")
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		switch section {
		case "pub":
			publishes = append(publishes, topic)
		case "sub":
			subscribes = append(subscribes, topic)
		}
	}
	return publishes, subscribes
}

// parseROS2ActionList parses `ros2 action list -t` output: lines of the form
// "/name [pkg/action/Type]" (same shape as `ros2 topic list -t`).
func parseROS2ActionList(out string) []*agentpbv2.ROS2Action {
	var actions []*agentpbv2.ROS2Action
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "/") {
			continue
		}
		name := line
		var types []string
		if open := strings.Index(line, " ["); open >= 0 && strings.HasSuffix(line, "]") {
			name = line[:open]
			for _, t := range strings.Split(line[open+2:len(line)-1], ",") {
				if t = strings.TrimSpace(t); t != "" {
					types = append(types, t)
				}
			}
		}
		actions = append(actions, &agentpbv2.ROS2Action{Name: name, Types: types})
	}
	return actions
}

// parseROS2ActionInfo extracts the action name plus its client and server node
// names from `ros2 action info <action>` output:
//
//	Action: /fibonacci
//	Action clients: 1
//	    /fibonacci_action_client
//	Action servers: 1
//	    /fibonacci_action_server
//
// Entry lines may carry a trailing " [type]" which is stripped from the name.
func parseROS2ActionInfo(out string) (name string, clients, servers []string) {
	section := ""
	for _, raw := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "Action:"):
			name = strings.TrimSpace(strings.TrimPrefix(trimmed, "Action:"))
			section = ""
			continue
		case strings.HasPrefix(trimmed, "Action clients:"):
			section = "clients"
			continue
		case strings.HasPrefix(trimmed, "Action servers:"):
			section = "servers"
			continue
		}
		if !strings.HasPrefix(trimmed, "/") {
			continue
		}
		node := trimmed
		if open := strings.Index(node, " ["); open >= 0 {
			node = node[:open]
		}
		switch section {
		case "clients":
			clients = append(clients, node)
		case "servers":
			servers = append(servers, node)
		}
	}
	return name, clients, servers
}

// parseROS2LifecycleState parses `ros2 lifecycle get <node>` output, e.g.
// "active [3]" → ("active", 3). A bare state with no id is also accepted.
func parseROS2LifecycleState(out string) (state string, id uint32, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if open := strings.Index(line, " ["); open >= 0 && strings.HasSuffix(line, "]") {
			if n, err := strconv.Atoi(strings.TrimSpace(line[open+2 : len(line)-1])); err == nil {
				return line[:open], uint32(n), true
			}
			return line[:open], 0, true
		}
		return line, 0, true
	}
	return "", 0, false
}

// parseROS2LifecycleTransitions parses `ros2 lifecycle list <node>` output:
//
//   - configure [1]
//     Start: unconfigured
//     Goal: configuring
//   - shutdown [5]
//     Start: unconfigured
//     Goal: shuttingdown
func parseROS2LifecycleTransitions(out string) []*agentpbv2.ROS2LifecycleTransition {
	var transitions []*agentpbv2.ROS2LifecycleTransition
	var current *agentpbv2.ROS2LifecycleTransition
	for _, raw := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if label, found := strings.CutPrefix(trimmed, "- "); found {
			t := &agentpbv2.ROS2LifecycleTransition{}
			if open := strings.Index(label, " ["); open >= 0 && strings.HasSuffix(label, "]") {
				if n, err := strconv.Atoi(strings.TrimSpace(label[open+2 : len(label)-1])); err == nil {
					t.Id = uint32(n)
				}
				label = label[:open]
			}
			t.Label = strings.TrimSpace(label)
			transitions = append(transitions, t)
			current = t
			continue
		}
		if current == nil {
			continue
		}
		if v, found := strings.CutPrefix(trimmed, "Start:"); found {
			current.StartState = strings.TrimSpace(v)
		} else if v, found := strings.CutPrefix(trimmed, "Goal:"); found {
			current.GoalState = strings.TrimSpace(v)
		}
	}
	return transitions
}

// parseROS2ComponentList parses `ros2 component list` output. Container node
// names are unindented "/name" lines; loaded components follow indented as
// "  <uid>  /node":
//
//	/ComponentManager
//	  1  /talker
//	  2  /listener
func parseROS2ComponentList(out string) []*agentpbv2.ROS2ComponentContainer {
	var containers []*agentpbv2.ROS2ComponentContainer
	var current *agentpbv2.ROS2ComponentContainer
	for _, raw := range strings.Split(out, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		// Container lines are unindented and start with "/".
		if !strings.HasPrefix(raw, " ") && !strings.HasPrefix(raw, "\t") && strings.HasPrefix(strings.TrimSpace(raw), "/") {
			current = &agentpbv2.ROS2ComponentContainer{Name: strings.TrimSpace(raw)}
			containers = append(containers, current)
			continue
		}
		if current == nil {
			continue
		}
		// Component entry lines: "<uid>  /node".
		fields := strings.Fields(raw)
		if len(fields) >= 2 {
			if n, err := strconv.Atoi(fields[0]); err == nil {
				current.Components = append(current.Components, &agentpbv2.ROS2LoadedComponent{
					Uid:  uint32(n),
					Name: fields[1],
				})
			}
		}
	}
	return containers
}

// parseROS2ComponentLoad parses `ros2 component load …` output, e.g.
// "Loaded node '/talker' as 1" → (1, "/talker").
func parseROS2ComponentLoad(out string) (uid uint32, nodeName string, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Loaded node") {
			continue
		}
		if q1 := strings.Index(line, "'"); q1 >= 0 {
			if q2 := strings.Index(line[q1+1:], "'"); q2 >= 0 {
				nodeName = line[q1+1 : q1+1+q2]
			}
		}
		if idx := strings.LastIndex(line, " as "); idx >= 0 {
			if n, err := strconv.Atoi(strings.TrimSpace(line[idx+4:])); err == nil {
				uid = uint32(n)
			}
		}
		return uid, nodeName, true
	}
	return 0, "", false
}

// parseROS2BagDurationNanos extracts the recording duration from a rosbag2
// metadata.yaml. It looks for the "nanoseconds:" entry following the
// "duration:" key.
func parseROS2BagDurationNanos(metadata string) (int64, bool) {
	inDuration := false
	for _, raw := range strings.Split(metadata, "\n") {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "duration:") {
			inDuration = true
			continue
		}
		if inDuration {
			if val, found := strings.CutPrefix(trimmed, "nanoseconds:"); found {
				n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
				if err != nil {
					return 0, false
				}
				return n, true
			}
			// Any other key ends the duration block.
			inDuration = false
		}
	}
	return 0, false
}
