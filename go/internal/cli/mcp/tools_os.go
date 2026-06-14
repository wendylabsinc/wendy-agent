package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func (s *mcpServer) registerOSTools(srv *server.MCPServer) {
	srv.AddTool(appsui.WithUI(mcpgo.NewTool("os_update",
		mcpgo.WithDescription("Trigger an OS update on the connected device and stream progress"),
		mcpgo.WithString("artifact_url",
			mcpgo.Description("URL of the OS update artifact (leave empty to use the device's configured update channel)"),
		),
		mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target.")),
	), WendyAppURI), s.handleOSUpdate)
}

func (s *mcpServer) handleOSUpdate(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn, err := s.resolveConn(ctx, stringParam(req, "device"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	stream, err := conn.AgentService.UpdateOS(ctx, &agentpb.UpdateOSRequest{
		ArtifactUrl: stringParam(req, "artifact_url"),
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}

	var sb strings.Builder
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return mcpgo.NewToolResultError(grpcErrString(err)), nil
		}
		switch resp.ResponseType.(type) {
		case *agentpb.UpdateOSResponse_Progress_:
			p := resp.GetProgress()
			sb.WriteString(fmt.Sprintf("[%s] %d%%\n", p.GetPhase(), p.GetPercent()))
		case *agentpb.UpdateOSResponse_Completed_:
			c := resp.GetCompleted()
			if c.GetRebootRequired() {
				sb.WriteString("update complete — reboot required\n")
			} else {
				sb.WriteString("update complete\n")
			}
		case *agentpb.UpdateOSResponse_Failed_:
			return mcpgo.NewToolResultError(fmt.Sprintf("update failed: %s", resp.GetFailed().GetErrorMessage())), nil
		}
	}
	out := sb.String()
	if out == "" {
		out = "OS update initiated"
	}
	res := mcpgo.NewToolResultText(out)
	deviceLabel := conn.Host
	return appsui.ResultWithUI(res, WendyAppURI, "controls", controlsData(deviceLabel, nil)), nil
}
