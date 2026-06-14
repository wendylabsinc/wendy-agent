package mcp

import "testing"

func TestAppEntryClassification(t *testing.T) {
	e := appEntry{Name: "cam", HasMCP: true, HasUI: true}
	if e.status() != "ui" {
		t.Fatalf("status = %q", e.status())
	}
	if (appEntry{Name: "api", HasMCP: true, HasUI: false}).status() != "tools" {
		t.Fatalf("tools-only misclassified")
	}
	if (appEntry{Name: "log", HasMCP: false}).status() != "none" {
		t.Fatalf("no-mcp misclassified")
	}
}
