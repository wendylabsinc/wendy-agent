package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestProgressToken_NilWhenNoMeta(t *testing.T) {
	if progressToken(mcpgo.CallToolRequest{}) != nil {
		t.Error("expected nil token when no _meta")
	}
}

func TestReportProgress_NoTokenNoPanic(t *testing.T) {
	// nil token + no server in ctx must be a safe no-op.
	reportProgress(context.Background(), nil, 1, 2, "x")
}

func TestReportProgress_NoServerNoPanic(t *testing.T) {
	// non-nil token but no server bound to ctx must not panic.
	reportProgress(context.Background(), "tok-1", 1, 2, "x")
}
