package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

func callProtocolTool(t *testing.T, srv *server.MCPServer, name string, args ...map[string]any) mcpgo.CallToolResult {
	t.Helper()
	encodedArgs := []byte("{}")
	if len(args) > 0 {
		encodedArgs, _ = json.Marshal(args[0])
	}
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, encodedArgs)
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

func TestBindingPresetStaysOffTheHost(t *testing.T) {
	var asked []string
	p := &BindingPolicy{Confirm: func(_ context.Context, tool string, _ map[string]any) bool {
		asked = append(asked, tool)
		return false
	}}
	srv := bindingServer(t, p)
	probes := []string{"os_install_verify", "project_validate", "os_list_drives", "os_install_plan", "os_install_status", "app_inspect", "device_list", "cloud_ping", "simulator_list"}
	for _, tool := range protocolTools(t, srv) {
		if slices.Contains(probes, tool.Name) {
			t.Errorf("the read-only preset advertises %s, which reaches the host", tool.Name)
		}
	}
	for _, name := range probes {
		if r := callProtocolTool(t, srv, name); !deniedByBinding(r) {
			t.Errorf("%s without --allow-tool: got %+v, want DENIED", name, r)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a refused tool must not reach confirmation; asked %v", asked)
	}

	p.Tools = probes
	for _, name := range probes {
		callProtocolTool(t, srv, name)
	}
	if !slices.Equal(asked, probes) {
		t.Fatalf("an allowed host tool must be confirmed like a state change; asked %v", asked)
	}
}

func TestBindingPinsTheStartingSession(t *testing.T) {
	const tenant, other = "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	session := func(name, endpoint, tenant string, org int) config.AuthConfig {
		return config.AuthConfig{Name: name, CloudGRPC: endpoint, APIKey: "k", Certificates: []config.CertificateInfo{{
			PrincipalURI: "spiffe://wendy.sh/tenant/" + tenant + "/operator/op", OrganizationID: org,
		}}}
	}
	pinned := session("work", "cloud.example:443", tenant, 7)
	cfg := &config.Config{CurrentContext: "home", Auth: []config.AuthConfig{
		pinned, session("home", "cloud.example:443", other, 8), session("lab", "lab.example:443", other, 9),
	}}
	var asked []string
	p := &BindingPolicy{AllowAll: true, Session: &pinned, Confirm: func(_ context.Context, tool string, _ map[string]any) bool {
		asked = append(asked, tool)
		return false
	}}
	s := New(cfg, nil)
	s.setBindingPolicy(p)
	srv, err := s.newProtocolServer()
	if err != nil {
		t.Fatal(err)
	}

	for name, args := range map[string]map[string]any{
		"cloud_discover": {"cloud_grpc": "lab.example:443"},
		"device_connect": {"device": "cloud://cloud.example:443/tenant/" + other + "/asset/" + tenant},
		"run":            {"device": "CLOUD://cloud.example:443/org/8/asset/3"},
	} {
		if r := callProtocolTool(t, srv, name, args); !deniedByBinding(r) || !strings.Contains(r.Content[0].(mcpgo.TextContent).Text, "another Wendy Cloud session") {
			t.Errorf("%s %v: got %+v, want DENIED for another session", name, args, r)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("a call naming another session must not reach confirmation; asked %v", asked)
	}
	for _, args := range []map[string]any{
		{"cloud_grpc": "cloud.example:443"},
		{"device": "cloud://cloud.example:443/tenant/" + tenant + "/asset/" + other},
		{"device": "cloud://cloud.example:443/org/7/asset/3"},
	} {
		if key, other := p.otherSession(args); other {
			t.Errorf("%v: %s refused, but it names the pinned session", args, key)
		}
	}

	// The default context is "home"; a relay-fed call still resolves "work".
	auth, err := s.cloudAuthEntry("")
	if err != nil || auth.Name != "work" {
		t.Fatalf("cloudAuthEntry = %v, %v; want the pinned session", auth, err)
	}
	cfg.Auth[0] = session("work", "cloud.example:443", other, 8) // re-login to another org under the same name
	if _, err := s.cloudAuthEntry(""); err == nil {
		t.Fatal("the pinned session changed organization; cloud calls must stop")
	}
}

func TestCheckToolNames(t *testing.T) {
	if err := CheckToolNames([]string{"device_disconnect", "os_install_verify"}); err != nil {
		t.Fatal(err)
	}
	err := CheckToolNames([]string{"device_disconect"})
	if err == nil || !strings.Contains(err.Error(), `"device_disconect"`) || !strings.Contains(err.Error(), "device_disconnect") {
		t.Fatalf("a typo must fail and list the valid names; got %v", err)
	}
}
