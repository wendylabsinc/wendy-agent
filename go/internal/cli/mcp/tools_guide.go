package mcp

import (
	"context"
	"encoding/json"
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
- provisioning_start / provisioning_status

Host↔device file sync happens automatically as part of ` + "`wendy run`" + `'s
fast redeploy path — there is no standalone file-sync CLI command or MCP tool.

## Deploying a workload

Use the run tool to build and deploy a local project to a cloud-enrolled device:
  run(project_path="/path/to/project", device_name="mydevice")

## Disconnecting

device_disconnect — closes the active connection and frees resources.

## Entitlements

Apps declare the device capabilities they need (gpu, network, persistence,
camera, bluetooth, etc.) in their project's wendy.json. The device only
grants what's declared there — if a container needs a capability that isn't
entitled, the operation fails (or the container exits) with error_code
ENTITLEMENT_DENIED, and container_list surfaces it in termination_reason for
stopped containers.

## Result shape & error codes

Tools that return data include a machine-readable structuredContent object
alongside human-readable text. Errors include an error_code you can branch
on — e.g. NOT_CONNECTED, DEVICE_UNREACHABLE, ENTITLEMENT_DENIED,
INVALID_ARGUMENT, NOT_FOUND, MULTIPLE_SESSIONS, UNSUPPORTED, TIMEOUT,
INTERNAL. Tool annotations mark read-only vs destructive vs mutating
operations and whether a tool reaches beyond the connected device (open-world).

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

// registerDiagnosticsResource registers wendy://diagnostics, which reports
// container-MCP proxy failures that would otherwise only appear on stderr.
func (s *mcpServer) registerDiagnosticsResource(srv *server.MCPServer) {
	srv.AddResource(
		mcpgo.NewResource("wendy://diagnostics", "Wendy MCP Diagnostics",
			mcpgo.WithResourceDescription("Container-MCP proxy failures recorded during this session (app name, stage, error, time), as JSON."),
			mcpgo.WithMIMEType("application/json"),
		),
		s.handleDiagnosticsResource,
	)
}

func (s *mcpServer) handleDiagnosticsResource(_ context.Context, _ mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
	data, err := json.Marshal(s.proxyDiagnostics())
	if err != nil {
		return nil, err
	}
	return []mcpgo.ResourceContents{
		mcpgo.TextResourceContents{URI: "wendy://diagnostics", MIMEType: "application/json", Text: string(data)},
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
