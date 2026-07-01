package foxglovecdr

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// ToYAML renders a value tree as compact flow-style YAML (which is a superset
// of JSON) suitable as the message argument to `ros2 topic pub` or
// `ros2 service call`, e.g. `{linear: {x: 1.0}, angular: {z: 0.5}}`.
//
// Flow style keeps the whole message on a single line, which is what the ros2
// CLI expects for the message positional argument.
func ToYAML(value map[string]any) (string, error) {
	node := &yaml.Node{}
	if err := node.Encode(value); err != nil {
		return "", err
	}
	// Force flow (inline) style recursively so mappings and sequences render as
	// `{...}` / `[...]` on one line.
	setFlowStyle(node)

	out, err := yaml.Marshal(node)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// setFlowStyle walks a yaml.Node tree and marks mappings and sequences as flow
// style.
func setFlowStyle(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		n.Style = yaml.FlowStyle
	}
	for _, c := range n.Content {
		setFlowStyle(c)
	}
}

// FromYAML parses a `ros2 service call` YAML response body (or any YAML/JSON
// mapping) into a value map. Integer scalars decode to int (widened by callers
// as needed) and floats to float64, matching gopkg.in/yaml.v3's defaults.
func FromYAML(s string) (map[string]any, error) {
	var out map[string]any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}
