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

func TestResourceUIMetaUsesObjectShapes(t *testing.T) {
	ui := resourceUIMeta(&UIResourceOptions{
		Permissions:       []string{"microphone"},
		CSPConnectDomains: []string{"https://api.example"},
	})
	// permissions must be an object ({"microphone":{}}), not an array — hosts
	// reject the array form with an invalid_type error.
	perms, ok := ui["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions should be an object, got %T", ui["permissions"])
	}
	if _, ok := perms["microphone"]; !ok {
		t.Fatalf("expected microphone permission, got %v", perms)
	}
	csp, ok := ui["csp"].(map[string]any)
	if !ok {
		t.Fatalf("csp should be an object, got %T", ui["csp"])
	}
	if got := csp["connectDomains"].([]string)[0]; got != "https://api.example" {
		t.Fatalf("csp connectDomains = %v", got)
	}
}

func TestResourceUIMetaNilWhenNothingRequested(t *testing.T) {
	if resourceUIMeta(&UIResourceOptions{Description: "x"}) != nil {
		t.Fatalf("expected nil _meta.ui when no csp/permissions requested")
	}
}
