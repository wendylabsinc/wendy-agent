package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	grpcclient "github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// appEntry is one row in the Apps launcher.
type appEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	HasMCP  bool   `json:"hasMcp"`
	HasUI   bool   `json:"hasUi"`
}

// status maps an entry to one of the launcher's three states.
func (e appEntry) status() string {
	switch {
	case e.HasMCP && e.HasUI:
		return "ui"
	case e.HasMCP:
		return "tools"
	default:
		return "none"
	}
}

func (s *mcpServer) registerAppsTools(srv *server.MCPServer) {
	list := appsui.WithUI(mcpgo.NewTool("apps_list",
		mcpgo.WithDescription("List the container apps on the connected device and whether each exposes an in-chat UI."),
		mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target.")),
	), WendyAppURI)
	srv.AddTool(list, s.handleAppsList)

	open := appsui.WithUI(mcpgo.NewTool("app_open",
		mcpgo.WithDescription("Open a container app's own MCP UI inside the Wendy app."),
		mcpgo.WithString("app", mcpgo.Required(), mcpgo.Description("Container app name to open.")),
		mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target.")),
	), WendyAppURI)
	srv.AddTool(open, s.handleAppOpen)
}

func (s *mcpServer) handleAppsList(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn, err := s.resolveConn(ctx, stringParam(req, "device"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	entries, err := s.listAppEntries(ctx, conn)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.Marshal(entries)
	res := mcpgo.NewToolResultText(string(b))
	host := conn.Host
	if host == "" {
		host = "device"
	}
	return appsui.ResultWithUI(res, WendyAppURI, "apps", map[string]any{
		"device": host,
		"apps":   entries,
	}), nil
}

// listAppEntries enumerates running containers and flags UI capability. A
// container is UI-capable when its proxied MCP server exposes a ui:// resource;
// Task 8 replaces containerHasUI with a real probe cached during proxy setup.
func (s *mcpServer) listAppEntries(ctx context.Context, conn *grpcclient.AgentConnection) ([]appEntry, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil, err
	}
	var out []appEntry
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		out = append(out, appEntry{
			Name:    c.GetAppName(),
			Version: c.GetAppVersion(),
			HasMCP:  c.GetMcpPort() > 0,
			HasUI:   s.containerHasUI(c.GetAppName(), c.GetMcpPort() > 0),
		})
	}
	return out, nil
}

// containerHasUI reports whether a container exposes a ui:// resource, using the
// cache populated during connectContainerMCPTools.
func (s *mcpServer) containerHasUI(app string, _ bool) bool {
	return s.getAppHasUI(app)
}

func (s *mcpServer) handleAppOpen(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	app := stringParam(req, "app")
	if app == "" {
		return mcpgo.NewToolResultError("app is required"), nil
	}
	uri := namespacedUIURI(app)
	res := mcpgo.NewToolResultText(fmt.Sprintf("opening %s", app))
	return appsui.ResultWithUI(res, uri, "app", map[string]any{"app": app}), nil
}
