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
