package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"
)

func (s *mcpServer) registerStatusTools(srv *server.MCPServer) {
	tool := appsui.WithUI(mcpgo.NewTool("wendy_status",
		mcpgo.WithDescription("Return current MCP session connection state and a plain-English suggested next step. Call this first to orient yourself."),
	), WendyAppURI)
	srv.AddTool(tool, s.handleWendyStatus)
}

func (s *mcpServer) handleWendyStatus(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	connType := s.GetConnType()

	if conn == nil {
		out := map[string]any{
			"connected":           false,
			"suggested_next_step": "not connected — call device_list to see available devices then device_connect, or cloud_discover + cloud_connect for cloud-enrolled devices",
		}
		b, _ := json.Marshal(out)
		return mcpgo.NewToolResultText(string(b)), nil
	}

	host := conn.Host
	if host == "" {
		host = "device"
	}
	out := map[string]any{
		"connected":           true,
		"device":              host,
		"connection_type":     connType,
		"suggested_next_step": fmt.Sprintf("connected to %s via %s — ready to use container, wifi, hardware, telemetry, and os tools", host, connType),
	}
	b, _ := json.Marshal(out)
	res := mcpgo.NewToolResultText(string(b))
	data := dashboardData(host, connType, s.deviceStats(ctx, conn), s.gatherContainers(ctx, conn))
	return appsui.ResultWithUI(res, WendyAppURI, "dashboard", data), nil
}
