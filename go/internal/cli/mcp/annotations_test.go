package mcp

import (
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func boolVal(t *testing.T, p *bool, name string) bool {
	t.Helper()
	if p == nil {
		t.Fatalf("%s hint is nil (unset)", name)
	}
	return *p
}

func TestReadOnly_NotDestructiveAndIdempotent(t *testing.T) {
	// readOnly must override mcp-go's pessimistic DestructiveHint=true default.
	tool := mcpgo.NewTool("t_ro", readOnly()...)
	if !boolVal(t, tool.Annotations.ReadOnlyHint, "ReadOnlyHint") {
		t.Error("readOnly() should set ReadOnlyHint=true")
	}
	if boolVal(t, tool.Annotations.DestructiveHint, "DestructiveHint") {
		t.Error("readOnly() should set DestructiveHint=false")
	}
	if !boolVal(t, tool.Annotations.IdempotentHint, "IdempotentHint") {
		t.Error("readOnly() should set IdempotentHint=true")
	}
}

func TestMutating_NotReadOnlyNotDestructive(t *testing.T) {
	tool := mcpgo.NewTool("t_mu", mutating()...)
	if boolVal(t, tool.Annotations.ReadOnlyHint, "ReadOnlyHint") {
		t.Error("mutating() should set ReadOnlyHint=false")
	}
	if boolVal(t, tool.Annotations.DestructiveHint, "DestructiveHint") {
		t.Error("mutating() should set DestructiveHint=false")
	}
}

func TestDestructive_ReadOnlyFalseDestructiveTrue(t *testing.T) {
	tool := mcpgo.NewTool("t_de", destructive()...)
	if boolVal(t, tool.Annotations.ReadOnlyHint, "ReadOnlyHint") {
		t.Error("destructive() should set ReadOnlyHint=false")
	}
	if !boolVal(t, tool.Annotations.DestructiveHint, "DestructiveHint") {
		t.Error("destructive() should set DestructiveHint=true")
	}
}

func TestWorldHints(t *testing.T) {
	local := mcpgo.NewTool("t_local", localOnly()...)
	if boolVal(t, local.Annotations.OpenWorldHint, "OpenWorldHint") {
		t.Error("localOnly() should set OpenWorldHint=false")
	}
	open := mcpgo.NewTool("t_open", openWorld()...)
	if !boolVal(t, open.Annotations.OpenWorldHint, "OpenWorldHint") {
		t.Error("openWorld() should set OpenWorldHint=true")
	}
}
