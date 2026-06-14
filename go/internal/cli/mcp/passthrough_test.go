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

// app_open must return the app's actual recorded ui:// URI (e.g. the camera
// feed at .../camera/feed), not a hardcoded /main that no real app registers.
func TestAppOpenUsesRecordedURI(t *testing.T) {
	s := New(nil, nil)
	// Simulate passthrough discovering the SecurityCam app's ui:// resource.
	s.setAppUI("security-cam", "ui://app/security-cam/camera/feed")

	if got := s.getAppUIURI("security-cam"); got != "ui://app/security-cam/camera/feed" {
		t.Fatalf("getAppUIURI = %q", got)
	}
	// An app with no recorded UI falls back to the /main convention.
	if got := s.getAppUIURI("unknown"); got != "" {
		t.Fatalf("expected empty URI for unknown app, got %q", got)
	}
}

// setAppUI keeps the first ui:// resource discovered (stable across re-scans).
func TestSetAppUIFirstWins(t *testing.T) {
	s := New(nil, nil)
	s.setAppUI("app", "ui://app/app/first")
	s.setAppUI("app", "ui://app/app/second")
	if got := s.getAppUIURI("app"); got != "ui://app/app/first" {
		t.Fatalf("first ui:// resource should win, got %q", got)
	}
	if !s.getAppHasUI("app") {
		t.Fatalf("setAppUI should mark the app UI-capable")
	}
}
