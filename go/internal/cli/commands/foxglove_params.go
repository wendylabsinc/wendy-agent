package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// Foxglove exposes ROS 2 parameters as flat "<node>:<param>" names. ROS 2 node
// names never contain ':', so splitting on the last ':' unambiguously recovers
// the node (which may contain '/') and the parameter (which may contain '.').
const fgParamSep = ":"

func fgSplitParamName(name string) (node, param string, ok bool) {
	i := strings.LastIndex(name, fgParamSep)
	if i <= 0 || i == len(name)-1 {
		return "", "", false
	}
	return name[:i], name[i+1:], true
}

func fgJoinParamName(node, param string) string { return node + fgParamSep + param }

// fgParamValueFromROS converts the output of `ros2 param get <node> <param>`
// into a Foxglove parameter value. That output is a human sentence, e.g.
// "Double value is: 1.5", "Boolean value is: True", "String value is: hi",
// "Integer values are: [1, 2, 3]". We strip the "... value(s) is/are:" prefix
// and interpret the remainder (Python-ish: True/False, single-quoted strings).
// Best-effort: an unrecognised form falls back to the trimmed string.
func fgParamValueFromROS(raw string) any {
	s := strings.TrimSpace(raw)
	// Strip the leading "<Type> value is:" / "<Type> values are:" label.
	for _, sep := range []string{" is: ", " are: "} {
		if i := strings.Index(s, sep); i >= 0 {
			s = strings.TrimSpace(s[i+len(sep):])
			break
		}
	}
	return fgInterpretParamScalar(s)
}

func fgInterpretParamScalar(s string) any {
	switch s {
	case "True", "true":
		return true
	case "False", "false":
		return false
	case "None", "":
		return nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		// Convert Python list syntax to JSON and parse: True/False -> true/false,
		// single quotes -> double quotes. Falls back to the raw string on failure.
		j := strings.NewReplacer("True", "true", "False", "false", "'", "\"").Replace(s)
		var arr []any
		if err := json.Unmarshal([]byte(j), &arr); err == nil {
			return arr
		}
		return s
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	// Strip surrounding quotes if present.
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// fgParamLiteralFromValue renders a Foxglove parameter value as the literal that
// `ros2 param set <node> <param> <literal>` parses. ros2 parses the argument as
// YAML; JSON is a YAML subset and is always compact and unambiguous, so we emit
// JSON (1.5, true, "hi", [1,2,3]).
func fgParamLiteralFromValue(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// getParameters answers a Foxglove getParameters request. An empty name list
// enumerates every parameter of every node via ListParams. Individual lookups
// that fail are skipped (logged) so one bad parameter never fails the batch.
func (s *foxgloveServer) getParameters(ctx context.Context, names []string, id string) fgParameterValues {
	if len(names) == 0 {
		names = s.allParamNames(ctx)
	}
	out := fgParameterValues{Op: "parameterValues", ID: id, Parameters: make([]fgParameter, 0, len(names))}
	for _, name := range names {
		node, param, ok := fgSplitParamName(name)
		if !ok {
			continue
		}
		resp, err := s.src.GetParam(ctx, &agentpbv2.GetROS2ParamRequest{DomainId: s.domainID, Node: node, Param: param})
		if err != nil {
			fmt.Fprintf(os.Stderr, "foxglove: get parameter %s: %v\n", name, ros2RPCError(err))
			continue
		}
		out.Parameters = append(out.Parameters, fgParameter{Name: name, Value: fgParamValueFromROS(resp.GetValue())})
	}
	return out
}

// allParamNames enumerates "<node>:<param>" for every parameter of every node.
func (s *foxgloveServer) allParamNames(ctx context.Context) []string {
	resp, err := s.src.ListParams(ctx, &agentpbv2.ListROS2ParamsRequest{DomainId: s.domainID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "foxglove: list parameters: %v\n", ros2RPCError(err))
		return nil
	}
	var names []string
	for _, n := range resp.GetNodes() {
		for _, p := range n.GetParams() {
			names = append(names, fgJoinParamName(n.GetNode(), p))
		}
	}
	return names
}

// setParameters applies a Foxglove setParameters request via SetParam. Failures
// are logged per-parameter and do not abort the batch. Returns the resulting
// current values (Foxglove echoes set results as parameterValues when an id is
// present).
func (s *foxgloveServer) setParameters(ctx context.Context, params []fgParameter, id string) fgParameterValues {
	applied := make([]string, 0, len(params))
	for _, p := range params {
		node, param, ok := fgSplitParamName(p.Name)
		if !ok {
			continue
		}
		literal, err := fgParamLiteralFromValue(p.Value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "foxglove: set parameter %s: %v\n", p.Name, err)
			continue
		}
		resp, err := s.src.SetParam(ctx, &agentpbv2.SetROS2ParamRequest{DomainId: s.domainID, Node: node, Param: param, Value: literal})
		if err != nil {
			fmt.Fprintf(os.Stderr, "foxglove: set parameter %s: %v\n", p.Name, ros2RPCError(err))
			continue
		}
		if !resp.GetSuccess() {
			fmt.Fprintf(os.Stderr, "foxglove: set parameter %s failed: %s\n", p.Name, resp.GetMessage())
			continue
		}
		applied = append(applied, p.Name)
	}
	return s.getParameters(ctx, applied, id)
}
