package mcp

import (
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestNamespacedUIURIRoundTrip(t *testing.T) {
	got := namespacedUIURI2("my-detector", "ui://main")
	want := "ui://app/my_detector/main"
	if got != want {
		t.Fatalf("namespaced = %q want %q", got, want)
	}
	app, inner, ok := parseNamespacedUIURI(want)
	if !ok || app != "my_detector" || inner != "ui://main" {
		t.Fatalf("parse = (%q,%q,%v)", app, inner, ok)
	}
}

func TestRewriteToolUIMetaIsNamespaced(t *testing.T) {
	tool := withRawUI("ui://main")
	rewriteToolUIMeta(&tool, "my-detector")
	ui := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://app/my_detector/main" {
		t.Fatalf("rewritten = %v", ui["resourceUri"])
	}
}

func withRawUI(uri string) mcpgo.Tool {
	t := mcpgo.NewTool("demo")
	t.Meta = &mcpgo.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"resourceUri": uri}}}
	return t
}
