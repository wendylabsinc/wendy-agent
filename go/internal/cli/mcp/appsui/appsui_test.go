package appsui

import (
	"bytes"
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestWithUISetsMetaResourceUri(t *testing.T) {
	tool := WithUI(mcpgo.NewTool("demo"), "ui://wendy/app")
	if tool.Meta == nil || tool.Meta.AdditionalFields["ui"] == nil {
		t.Fatalf("expected _meta.ui to be set")
	}
	ui := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://wendy/app" {
		t.Fatalf("got resourceUri %v", ui["resourceUri"])
	}
}

func TestResultWithUISetsViewAndData(t *testing.T) {
	res := ResultWithUI(mcpgo.NewToolResultText("ok"), "ui://wendy/app", "dashboard", map[string]any{"x": 1})
	sc := res.StructuredContent.(map[string]any)
	if sc["view"] != "dashboard" {
		t.Fatalf("got view %v", sc["view"])
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	meta := raw["_meta"].(map[string]any)
	ui := meta["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://wendy/app" {
		t.Fatalf("got resourceUri %v", ui["resourceUri"])
	}
}

func TestWendyAppHTMLEmbedded(t *testing.T) {
	if len(WendyAppHTML) < 100 {
		t.Fatalf("embedded UI bundle looks empty: %d bytes", len(WendyAppHTML))
	}
	if !bytes.Contains(WendyAppHTML, []byte("ui/initialize")) {
		t.Fatalf("UI bundle missing postMessage ui/ bridge")
	}
}

func TestUIMetaIncludesCSPAndPermissions(t *testing.T) {
	m := uiMeta("ui://x", &UIResourceOptions{CSP: []string{"https://cdn.example"}, Permissions: []string{"camera"}})
	if got := m["csp"].([]string)[0]; got != "https://cdn.example" {
		t.Fatalf("csp = %v", got)
	}
	if got := m["permissions"].([]string)[0]; got != "camera" {
		t.Fatalf("permissions = %v", got)
	}
}
