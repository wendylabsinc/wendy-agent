package commands

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestFGSplitParamName(t *testing.T) {
	node, param, ok := fgSplitParamName("/talker:publish_rate")
	if !ok || node != "/talker" || param != "publish_rate" {
		t.Fatalf("split = (%q,%q,%v)", node, param, ok)
	}
	// Parameter names may contain '.'; node is everything before the last ':'.
	node, param, ok = fgSplitParamName("/ns/node:qos.depth")
	if !ok || node != "/ns/node" || param != "qos.depth" {
		t.Fatalf("split nested = (%q,%q,%v)", node, param, ok)
	}
	for _, bad := range []string{"noseparator", ":leading", "trailing:"} {
		if _, _, ok := fgSplitParamName(bad); ok {
			t.Errorf("fgSplitParamName(%q) should fail", bad)
		}
	}
}

func TestFGParamValueFromROS(t *testing.T) {
	cases := map[string]any{
		"Double value is: 1.5":            1.5,
		"Integer value is: 42":            int64(42),
		"Boolean value is: True":          true,
		"Boolean value is: False":         false,
		"String value is: hello":          "hello",
		"Integer values are: [1, 2, 3]":   []any{float64(1), float64(2), float64(3)},
		"String values are: ['a', 'b']":   []any{"a", "b"},
		"1.5":                             1.5, // no label prefix
	}
	for in, want := range cases {
		if got := fgParamValueFromROS(in); !reflect.DeepEqual(got, want) {
			t.Errorf("fgParamValueFromROS(%q) = %#v, want %#v", in, got, want)
		}
	}
}

func TestFGParamLiteralFromValue(t *testing.T) {
	cases := map[string]any{
		"1.5":       1.5,
		"true":      true,
		"\"hi\"":    "hi",
		"[1,2,3]":   []any{1, 2, 3},
	}
	for want, v := range cases {
		got, err := fgParamLiteralFromValue(v)
		if err != nil {
			t.Fatalf("literal(%v): %v", v, err)
		}
		if got != want {
			t.Errorf("fgParamLiteralFromValue(%v) = %q, want %q", v, got, want)
		}
	}
}

// paramSource records SetParam calls and answers GetParam/ListParams from a map.
type paramSource struct {
	fakeFGSource
	params map[string]string // "<node>:<param>" -> `ros2 param get` output
	sets   map[string]string // "<node>:<param>" -> literal passed to SetParam
}

func (s *paramSource) ListParams(context.Context, *agentpbv2.ListROS2ParamsRequest, ...grpc.CallOption) (*agentpbv2.ListROS2ParamsResponse, error) {
	byNode := map[string][]string{}
	for full := range s.params {
		node, param, _ := fgSplitParamName(full)
		byNode[node] = append(byNode[node], param)
	}
	resp := &agentpbv2.ListROS2ParamsResponse{}
	for node, params := range byNode {
		sort.Strings(params)
		resp.Nodes = append(resp.Nodes, &agentpbv2.ListROS2ParamsResponse_NodeParams{Node: node, Params: params})
	}
	return resp, nil
}

func (s *paramSource) GetParam(_ context.Context, in *agentpbv2.GetROS2ParamRequest, _ ...grpc.CallOption) (*agentpbv2.GetROS2ParamResponse, error) {
	return &agentpbv2.GetROS2ParamResponse{Value: s.params[fgJoinParamName(in.GetNode(), in.GetParam())]}, nil
}

func (s *paramSource) SetParam(_ context.Context, in *agentpbv2.SetROS2ParamRequest, _ ...grpc.CallOption) (*agentpbv2.SetROS2ParamResponse, error) {
	if s.sets == nil {
		s.sets = map[string]string{}
	}
	s.sets[fgJoinParamName(in.GetNode(), in.GetParam())] = in.GetValue()
	return &agentpbv2.SetROS2ParamResponse{Success: true}, nil
}

func TestFoxgloveGetParameters(t *testing.T) {
	src := &paramSource{params: map[string]string{
		"/talker:rate":         "Double value is: 10.0",
		"/talker:use_sim_time": "Boolean value is: False",
	}}
	srv := &foxgloveServer{src: src}

	// Named lookup.
	vals := srv.getParameters(context.Background(), []string{"/talker:rate"}, "req1")
	if vals.Op != "parameterValues" || vals.ID != "req1" || len(vals.Parameters) != 1 {
		t.Fatalf("getParameters = %+v", vals)
	}
	if vals.Parameters[0].Name != "/talker:rate" || vals.Parameters[0].Value != 10.0 {
		t.Fatalf("param = %+v", vals.Parameters[0])
	}

	// Empty list enumerates all parameters.
	all := srv.getParameters(context.Background(), nil, "")
	if len(all.Parameters) != 2 {
		t.Fatalf("enumerate = %d params, want 2", len(all.Parameters))
	}
}

func TestFoxgloveSetParameters(t *testing.T) {
	src := &paramSource{params: map[string]string{"/talker:rate": "Double value is: 10.0"}}
	srv := &foxgloveServer{src: src}
	srv.setParameters(context.Background(), []fgParameter{{Name: "/talker:rate", Value: 25.0}}, "")
	if got := src.sets["/talker:rate"]; got != "25" {
		t.Fatalf("SetParam literal = %q, want 25", got)
	}
}

func TestFGParseClientMessage_Parameters(t *testing.T) {
	get, err := fgParseClientMessage([]byte(`{"op":"getParameters","parameterNames":["/n:a","/n:b"],"id":"x"}`))
	if err != nil || get.Op != "getParameters" || len(get.ParameterNames) != 2 || get.ID != "x" {
		t.Fatalf("getParameters parse = %+v, err %v", get, err)
	}
	set, err := fgParseClientMessage([]byte(`{"op":"setParameters","parameters":[{"name":"/n:a","value":1.5}]}`))
	if err != nil || set.Op != "setParameters" || len(set.Parameters) != 1 || set.Parameters[0].Value != 1.5 {
		t.Fatalf("setParameters parse = %+v, err %v", set, err)
	}
}
