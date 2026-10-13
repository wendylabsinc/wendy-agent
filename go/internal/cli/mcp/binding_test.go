package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func bindingServer(t *testing.T, p *BindingPolicy) *server.MCPServer {
	t.Helper()
	s := New(&config.Config{}, nil)
	s.setBindingPolicy(p)
	srv, err := s.newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func callProtocolTool(t *testing.T, srv *server.MCPServer, name string) mcpgo.CallToolResult {
	t.Helper()
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, name)
	encoded, _ := json.Marshal(srv.HandleMessage(context.Background(), []byte(msg)))
	var reply struct{ Result mcpgo.CallToolResult }
	if err := json.Unmarshal(encoded, &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Result
}

func deniedByBinding(r mcpgo.CallToolResult) bool {
	content, _ := r.StructuredContent.(map[string]any)
	code, _ := content["error_code"].(string)
	return r.IsError && code == string(errCodeDenied)
}

func TestBindingAllowListRefusesUnlistedTools(t *testing.T) {
	var asked []string
	p := &BindingPolicy{Confirm: func(_ context.Context, tool string, _ map[string]any) bool {
		asked = append(asked, tool)
		return true
	}}
	srv := bindingServer(t, p)

	if r := callProtocolTool(t, srv, "device_disconnect"); !deniedByBinding(r) {
		t.Fatalf("device_disconnect outside the read-only preset: got %+v, want DENIED", r)
	}
	if len(asked) != 0 {
		t.Fatalf("a refused tool must not reach confirmation; asked %v", asked)
	}
	for _, tool := range protocolTools(t, srv) {
		if tool.Name == "device_disconnect" {
			t.Fatal("tools/list advertises a tool the binding does not allow")
		}
	}

	p.Tools = []string{"device_disconnect"}
	if r := callProtocolTool(t, srv, "device_disconnect"); deniedByBinding(r) {
		t.Fatalf("--allow-tool device_disconnect: still denied: %+v", r)
	}
	if !slices.Equal(asked, []string{"device_disconnect"}) {
		t.Fatalf("an allowed mutating tool must be confirmed; asked %v", asked)
	}
}

func TestBindingConfirmsMutatingCallsNotReads(t *testing.T) {
	var asked []string
	srv := bindingServer(t, &BindingPolicy{AllowAll: true, Confirm: func(_ context.Context, tool string, _ map[string]any) bool {
		asked = append(asked, tool)
		return false
	}})

	if r := callProtocolTool(t, srv, "wendy_status"); deniedByBinding(r) {
		t.Fatalf("read-only wendy_status was refused: %+v", r)
	}
	if len(asked) != 0 {
		t.Fatalf("a read must not ask for confirmation; asked %v", asked)
	}
	if r := callProtocolTool(t, srv, "device_disconnect"); !deniedByBinding(r) {
		t.Fatalf("unconfirmed device_disconnect: got %+v, want DENIED", r)
	}
	if !slices.Equal(asked, []string{"device_disconnect"}) {
		t.Fatalf("asked %v, want [device_disconnect]", asked)
	}

	// AAA §5.1 high-impact actions (deploy, container control, config push,
	// tunnel open, unenroll) and other state changes must never pass as
	// read-only, or a bound web client could run them unconfirmed.
	for _, name := range []string{"run", "container_start", "container_stop", "container_delete", "container_exec",
		"cloud_tunnel", "cloud_enroll_device", "device_update_agent", "os_update", "os_install_start",
		"wifi_connect", "camera_set_control", "auth_login"} {
		if readOnlyTool(srv.GetTool(name).Tool) {
			t.Errorf("%s is read-only, so a bound web client could call it unconfirmed", name)
		}
	}
}

func TestReadOnlyToolIgnoresProxiedAnnotations(t *testing.T) {
	proxied := mcpgo.NewTool("myapp_reset", readOnly()...)
	if readOnlyTool(proxied) {
		t.Fatal("a container tool's own read-only claim must not skip confirmation")
	}
}

func TestRealmAllowsMCP(t *testing.T) {
	token := func(claims string) *config.AuthConfig {
		return &config.AuthConfig{Name: "work", APIKey: "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".sig"}
	}
	if err := RealmAllowsMCP(token(`{"allow_mcp":false}`)); err == nil {
		t.Fatal("allow_mcp=false must refuse MCP")
	}
	for name, auth := range map[string]*config.AuthConfig{
		"claim true":    token(`{"allow_mcp":true}`),
		"claim absent":  token(`{"sub":"u"}`),
		"personal key":  {APIKey: "wendy_pat_abc"},
		"not logged in": nil,
	} {
		if err := RealmAllowsMCP(auth); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
