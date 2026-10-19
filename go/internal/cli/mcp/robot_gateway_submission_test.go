package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

func TestStandaloneCloudCatalogDeclaresPermissionsAndUI(t *testing.T) {
	cfg := linkedGatewayConfig(t)
	cfg.HTTP.ResourceURL = "https://mcp.wendy.example"
	cfg.HTTP.CloudLink.Scopes = []string{RobotReadScope, RobotCameraScope, RobotControlScope, RobotSettingsScope}
	g, err := NewRobotGateway(cfg, func(context.Context, string) (*grpcclient.AgentConnection, error) {
		t.Fatal("catalog inspection must not use an operator's local connection")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.httpHandler(http.DefaultClient, func(string) string { return base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) }); err != nil {
		t.Fatal(err)
	}
	defer g.CloseCloudLink()
	ctx := context.WithValue(context.Background(), gatewayPrincipalKey{}, gatewayPrincipal{"linked-account", cfg.HTTP.CloudLink.Scopes})
	response, handled := g.handleGatewayMCP2(ctx, gatewayModernRequest(1, "tools/list", nil), "")
	reply := gatewayModernEnvelope(t, response)
	if !handled || reply["error"] != nil {
		t.Fatalf("tools/list: %v", reply)
	}
	want := map[string]string{
		"identify_device": RobotReadScope, "list_robots": RobotReadScope, "open_robot": RobotReadScope,
		"open_devices": RobotReadScope, "inspect_robot": RobotReadScope, "search_devices": RobotReadScope,
		"read_device_metrics": RobotReadScope, "read_device_logs": RobotReadScope, "read_device_settings": RobotReadScope,
		"update_device_settings": RobotSettingsScope, "capture_robot_image": RobotCameraScope,
		"start_camera_preview": RobotCameraScope, "read_camera_preview": RobotCameraScope, "stop_camera_preview": RobotCameraScope,
		"start_robot_app": RobotControlScope, "stop_robot_app": RobotControlScope,
	}
	rows := reply["result"].(map[string]any)["tools"].([]any)
	if len(rows) != len(want) {
		t.Fatalf("standalone catalog has %d tools, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		tool := row.(map[string]any)
		name := tool["name"].(string)
		scope, ok := want[name]
		if !ok {
			t.Fatalf("standalone catalog exposed unsupported operation %s", name)
		}
		meta := tool["_meta"].(map[string]any)
		if _, ok := tool["outputSchema"].(map[string]any); !ok {
			t.Errorf("%s returns structured content without an output schema", name)
		}
		schemes := []any{map[string]any{"type": "oauth2", "scopes": []any{scope}}}
		if !reflect.DeepEqual(tool["securitySchemes"], schemes) || !reflect.DeepEqual(meta["securitySchemes"], schemes) {
			t.Errorf("%s has inconsistent OAuth permissions: %v", name, tool)
		}
		annotations := tool["annotations"].(map[string]any)
		for _, hint := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
			if _, ok := annotations[hint].(bool); !ok {
				t.Errorf("%s lacks explicit %s", name, hint)
			}
		}
		if annotations["openWorldHint"] != false {
			t.Errorf("%s is confined to the linked account's inventory", name)
		}
		if scope != RobotReadScope && annotations["readOnlyHint"] != false {
			t.Errorf("%s modifies settings, app state, or camera capture", name)
		}
		if name == "update_device_settings" || name == "stop_camera_preview" || scope == RobotControlScope {
			if annotations["destructiveHint"] != true {
				t.Errorf("%s must disclose overwriting or cancellation", name)
			}
		}
	}
	for _, uri := range []string{robotPanelURI, "ui://wendy/robot-v1.html"} {
		response, _ := g.handleGatewayMCP2(ctx, gatewayModernRequest(2, "resources/read", map[string]any{"uri": uri}), "")
		reply := gatewayModernEnvelope(t, response)
		if reply["error"] != nil {
			t.Fatalf("resources/read: %v", reply)
		}
		contents := reply["result"].(map[string]any)["contents"].([]any)
		ui := contents[0].(map[string]any)["_meta"].(map[string]any)["ui"].(map[string]any)
		if ui["domain"] != "https://mcp.wendy.example" {
			t.Errorf("missing dedicated plugin origin: %v", ui)
		}
		csp := ui["csp"].(map[string]any)
		if connections, ok := csp["connectDomains"].([]any); !ok || len(connections) != 0 {
			t.Errorf("standalone UI must not expose laptop loopback access: %v", csp)
		}
	}
}

func TestGatewayHTTPConfiguredResourceRoutesAndOAuthToolMetadata(t *testing.T) {
	for _, resource := range []string{"https://robots.example", "https://robots.example/", "https://robots.example/mcp"} {
		t.Run(resource, func(t *testing.T) {
			cfg := gatewayTestConfig()
			cfg.HTTP = &GatewayHTTPConfig{ResourceURL: resource, OAuth: &GatewayOAuthConfig{Issuer: "https://auth.example", IntrospectionURL: "https://auth.example/introspect", ClientID: "gateway", ClientSecretEnv: "SECRET"}}
			g, err := NewRobotGateway(cfg, func(context.Context, string) (*grpcclient.AgentConnection, error) {
				return nil, fmt.Errorf("unexpected device access")
			})
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: gatewayRoundTrip(func(*http.Request) (*http.Response, error) {
				claims, _ := json.Marshal(map[string]any{"active": true, "sub": "alice", "iss": cfg.HTTP.OAuth.Issuer, "aud": resource, "exp": time.Now().Add(time.Hour).Unix(), "scope": RobotReadScope, "token_type": "Bearer"})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(claims)))}, nil
			})}
			handler, err := g.httpHandler(client, func(string) string { return "secret" })
			if err != nil {
				t.Fatal(err)
			}
			path, metadataPath := "/", "/.well-known/oauth-protected-resource"
			if strings.HasSuffix(resource, "/mcp") {
				path, metadataPath = "/mcp", metadataPath+"/mcp"
			}
			request := func(path, token, origin string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "https://robots.example"+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("Accept", "application/json, text/event-stream")
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				r.Header.Set("Origin", origin)
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			unauthorized := request(path, "", "")
			if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Header().Get("WWW-Authenticate"), "https://robots.example"+metadataPath) {
				t.Fatalf("wrong OAuth challenge: %d %v", unauthorized.Code, unauthorized.Header())
			}
			for _, other := range []string{"/unrelated", "/%6dcp", "/mcp/", "/oauth/unknown"} {
				if w := request(other, "opaque-token", ""); w.Code != http.StatusNotFound {
					t.Errorf("unexpected route %q accepted: %d", other, w.Code)
				}
			}
			if w := request(path, "opaque-token", "https://attacker.example"); w.Code != http.StatusForbidden {
				t.Error("untrusted Origin accepted")
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "https://robots.example"+metadataPath, nil))
			var metadata map[string]any
			if json.Unmarshal(w.Body.Bytes(), &metadata) != nil || metadata["resource"] != resource {
				t.Fatalf("protected-resource discovery changed the OAuth audience: %s", w.Body.String())
			}
			w = request(path, "opaque-token", "")
			var reply struct {
				Result struct {
					Tools []map[string]any `json:"tools"`
				} `json:"result"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reply) != nil || len(reply.Result.Tools) == 0 {
				t.Fatalf("tools/list failed: %d %s", w.Code, w.Body.String())
			}
			for _, tool := range reply.Result.Tools {
				meta := tool["_meta"].(map[string]any)
				if tool["securitySchemes"] == nil || !reflect.DeepEqual(tool["securitySchemes"], meta["securitySchemes"]) {
					t.Errorf("HTTP catalog lost canonical or mirrored OAuth metadata: %v", tool)
				}
			}
		})
	}
}

func TestGatewayPublicModelAndOverwriteAnnotations(t *testing.T) {
	g, err := NewRobotGateway(gatewayTestConfig(), func(context.Context, string) (*grpcclient.AgentConnection, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"deploy_yolo_detector", "stop_yolo_detector", "configure_device_trigger", "write_workspace_file", "cancel_deployment_job"} {
		tool := g.protocol.ListTools()[name].Tool
		if boolVal(t, tool.Annotations.ReadOnlyHint, "readOnlyHint") || !boolVal(t, tool.Annotations.DestructiveHint, "destructiveHint") {
			t.Errorf("%s overwrites data or cancels active work", name)
		}
	}
	if !boolVal(t, g.protocol.ListTools()["deploy_yolo_detector"].Tool.Annotations.OpenWorldHint, "openWorldHint") {
		t.Fatal("public Hugging Face model downloads must be open-world")
	}
	// Local stdio pilots do not advertise a hosted OAuth provider.
	ctx := context.WithValue(context.Background(), gatewayPrincipalKey{}, gatewayPrincipal{"alice", robotGatewayScopes})
	for _, tool := range g.filterTools(ctx, []mcpgo.Tool{g.protocol.ListTools()["list_robots"].Tool}) {
		if tool.Meta.AdditionalFields["securitySchemes"] != nil {
			t.Fatal("development-token mode advertised OAuth")
		}
	}
}
