package mcp

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/assets"
)

const guideText = `Wendy MCP Guide
===============

Wendy manages remote Linux devices (edge servers, embedded boards, cloud VMs).
Every MCP session has at most one active device connection at a time.

## Getting started

Call wendy_status first — it tells you whether you are connected and what to do next.

## Connecting to a device

Local/direct devices:
1. device_list          — lists devices from config (add scan=true for live mDNS scan)
2. device_connect       — connect by address (host:port), e.g. "mydevice.local:50051"

Cloud-enrolled devices:
1. cloud_discover       — finds devices enrolled in your Wendy Cloud account
2. cloud_connect        — opens a secure tunnel to a cloud device by name

## Tools available once connected

- container_list / container_start / container_stop / container_delete / container_attach / container_stats
- wifi_list / wifi_connect / wifi_disconnect / wifi_status / wifi_known_networks
- telemetry_logs / telemetry_metrics / telemetry_traces
- hardware_capabilities
- os_update
- filesync_sync
- provisioning_start / provisioning_status

## Deploying a workload

Use the run tool to build and deploy a local project to a cloud-enrolled device:
  run(project_path="/path/to/project", device_name="mydevice")

## Disconnecting

device_disconnect — closes the active connection and frees resources.

## Documentation

Detailed documentation is available as MCP resources under wendy://docs/.
Run resources/list to see all available docs.
`

func (s *mcpServer) registerGuideResource(srv *server.MCPServer) {
	srv.AddResource(
		mcpgo.NewResource("wendy://guide", "Wendy Guide",
			mcpgo.WithResourceDescription("Overview of Wendy MCP tools, connection model, and common workflows. Read this first."),
			mcpgo.WithMIMEType("text/plain"),
		),
		s.handleGuideResource,
	)
	s.registerDocResources(srv)
}

func (s *mcpServer) handleGuideResource(_ context.Context, _ mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{URI: "wendy://guide", MIMEType: "text/plain", Text: guideText},
	}, nil
}

func (s *mcpServer) registerDocResources(srv *server.MCPServer) {
	_ = fs.WalkDir(assets.FS, "docs", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if path.Ext(p) != ".md" {
			return nil
		}
		relPath := strings.TrimPrefix(p, "docs/")
		uri := "wendy://docs/" + relPath
		name := docTitle(relPath)
		resource := mcpgo.NewResource(uri, name,
			mcpgo.WithResourceDescription(fmt.Sprintf("Wendy documentation: %s", relPath)),
			mcpgo.WithMIMEType("text/markdown"),
		)
		embeddedPath := p
		srv.AddResource(resource, func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
			data, readErr := assets.FS.ReadFile(embeddedPath)
			if readErr != nil {
				return nil, readErr
			}
			return []mcpgo.ResourceContents{
				mcpgo.TextResourceContents{URI: req.Params.URI, MIMEType: "text/markdown", Text: string(data)},
			}, nil
		})
		return nil
	})
}

// docTitle converts a relative doc path to a human-readable title.
func docTitle(relPath string) string {
	base := path.Base(relPath)
	base = strings.TrimSuffix(base, ".md")
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.ReplaceAll(base, "_", " ")
	dir := path.Dir(relPath)
	if dir == "." {
		return base
	}
	return dir + " / " + base
}
