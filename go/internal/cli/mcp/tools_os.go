package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func (s *mcpServer) registerOSTools(srv *server.MCPServer) {
	srv.AddTool(mcpgo.NewTool("os_update",
		mcpgo.WithDescription("Trigger an OS update on the connected device and stream progress"),
		mcpgo.WithString("artifact_url",
			mcpgo.Description("URL of the OS update artifact (leave empty to use the device's configured update channel)"),
		),
		mcpgo.WithString("updater",
			mcpgo.Description("OS update backend: auto (default; prefer wendyos-update, fall back to mender), wendyos, or mender"),
		),
	), s.handleOSUpdate)

	srv.AddTool(mcpgo.NewTool("os_update_status",
		mcpgo.WithDescription("Report the outcome of the device's most recent OS update: committed after post-reboot healthchecks, or rolled back with the services that failed"),
	), s.handleOSUpdateStatus)
}

func (s *mcpServer) handleOSUpdateStatus(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	resp, err := conn.AgentService.GetOSUpdateStatus(ctx, &agentpb.GetOSUpdateStatusRequest{})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	if !resp.GetHasResult() {
		return mcpgo.NewToolResultText("no OS update result recorded on this device"), nil
	}

	var sb strings.Builder
	outcome := strings.TrimPrefix(resp.GetOutcome().String(), "OUTCOME_")
	sb.WriteString(fmt.Sprintf("outcome: %s\n", strings.ToLower(outcome)))
	if v := resp.GetOldOsVersion(); v != "" {
		sb.WriteString(fmt.Sprintf("old OS version: %s\n", v))
	}
	if v := resp.GetNewOsVersion(); v != "" {
		sb.WriteString(fmt.Sprintf("new OS version: %s\n", v))
	}
	for _, svc := range resp.GetServices() {
		status := strings.ToLower(strings.TrimPrefix(svc.GetStatus().String(), "STATUS_"))
		if reason := svc.GetReason(); reason != "" {
			sb.WriteString(fmt.Sprintf("service %s: %s (%s)\n", svc.GetUnit(), status, reason))
		} else {
			sb.WriteString(fmt.Sprintf("service %s: %s\n", svc.GetUnit(), status))
		}
	}
	if re := resp.GetRollbackError(); re != "" {
		sb.WriteString(fmt.Sprintf("rollback error: %s\n", re))
	}
	return mcpgo.NewToolResultText(strings.TrimRight(sb.String(), "\n")), nil
}

func (s *mcpServer) handleOSUpdate(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	stream, err := conn.AgentService.UpdateOS(ctx, &agentpb.UpdateOSRequest{
		ArtifactUrl:    stringParam(req, "artifact_url"),
		UpdaterBackend: stringParam(req, "updater"),
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
	return mcpgo.NewToolResultText(out), nil
}
