package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// progressToken returns the client-supplied progress token for this request,
// or nil if the client did not request progress.
func progressToken(req mcpgo.CallToolRequest) any {
	if req.Params.Meta == nil {
		return nil
	}
	return req.Params.Meta.ProgressToken
}

// reportProgress emits an MCP progress notification for token. It is a silent
// no-op when token is nil or no server is bound to ctx, and it never returns an
// error — progress is best-effort telemetry, not part of a tool's contract.
func reportProgress(ctx context.Context, token any, progress, total float64, message string) {
	if token == nil {
		return
	}
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return
	}
	params := map[string]any{
		"progressToken": token,
		"progress":      progress,
	}
	if total > 0 {
		params["total"] = total
	}
	if message != "" {
		params["message"] = message
	}
	_ = srv.SendNotificationToClient(ctx, "notifications/progress", params)
}
