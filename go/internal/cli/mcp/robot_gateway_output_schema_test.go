package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func assertGatewayOutputSchema(t *testing.T, tool mcpgo.Tool, result *mcpgo.CallToolResult) {
	t.Helper()
	encoded, err := json.Marshal(tool)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(encoded, &descriptor); err != nil {
		t.Fatal(err)
	}
	schema, ok := descriptor["outputSchema"]
	if !ok {
		t.Fatalf("%s has no output schema", tool.Name)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:wendy:output", schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("urn:wendy:output")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if err := compiled.Validate(value); err != nil {
		t.Fatalf("%s output does not match its descriptor: %v\n%s", tool.Name, err, encoded)
	}
}

func TestGatewayOutputSchemasMatchDeviceAndWorkspaceResults(t *testing.T) {
	addr, _ := gatewayFixture(t, "schema-fixture")
	cfg := gatewayTestConfig()
	cfg.StateDirectory = t.TempDir()
	g, err := NewRobotGateway(cfg, func(context.Context, string) (*grpcclient.AgentConnection, error) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, err
		}
		return grpcclient.NewFromConn(conn), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), gatewayPrincipalKey{}, gatewayPrincipal{"alice", robotGatewayScopes})
	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"list_robots", nil}, {"open_devices", nil}, {"open_robot", map[string]any{"robot_id": "alpha"}},
		{"search_devices", map[string]any{"query": ""}}, {"identify_device", map[string]any{"robot_id": "alpha"}},
		{"inspect_robot", map[string]any{"robot_id": "alpha"}},
		{"start_robot_app", map[string]any{"robot_id": "alpha", "app_name": "companion"}},
		{"stop_robot_app", map[string]any{"robot_id": "alpha", "app_name": "companion"}},
		{"read_device_settings", nil}, {"update_device_settings", map[string]any{"set": map[string]any{"include_offline": true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := g.protocol.ListTools()[test.name]
			result, err := entry.Handler(ctx, callToolReq(test.name, test.args))
			if err != nil || result.IsError {
				t.Fatalf("fixture call failed: %v %v", result, err)
			}
			assertGatewayOutputSchema(t, entry.Tool, result)
		})
	}
	// The inspection budget can replace inventories with a bounded envelope.
	assertGatewayOutputSchema(t, g.protocol.ListTools()["inspect_robot"].Tool, okResultBounded(map[string]any{"large": "fixture content"}, 1))
}

func TestGatewayOutputSchemasMatchTelemetryAndCaptureResults(t *testing.T) {
	opts := telemetryOptionsValue{format: "compact", maxBytes: 16384, maxRecords: 30, maxBatches: 20}
	for _, test := range []struct {
		name   string
		result *mcpgo.CallToolResult
	}{
		{"read_device_logs", telemetryResult("logs", nil, opts)},
		{"read_device_metrics", telemetryMetricsHistoryResult(nil, opts, 20, 0)},
	} {
		tool := gatewayTool(test.name, "fixture", readOnly())
		assertGatewayOutputSchema(t, tool, test.result)
	}
	result, err := cameraSnapshotResult(context.Background(), callToolReq("camera_snapshot", map[string]any{"device_id": 3}), func(context.Context, *agentpb.StreamVideoRequest) (cameraSnapshot, error) {
		return cameraSnapshot{jpeg: snapshotTestJPEG(t, 16, 12), width: 16, height: 12}, nil
	})
	if err != nil || result.IsError {
		t.Fatalf("snapshot fixture failed: %v %v", result, err)
	}
	data := structuredMap(t, result)
	data["robot_id"], data["camera_id"] = "alpha", 3
	assertGatewayOutputSchema(t, gatewayTool("capture_robot_image", "fixture", mutating()), result)
}
