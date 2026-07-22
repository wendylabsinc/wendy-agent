package mcp

import (
	"context"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerPrompts registers the workflow prompts that walk a client through
// common multi-tool sequences (deploy, diagnose, provision). Handlers are
// pure templating: they never touch the device connection or gRPC.
func (s *mcpServer) registerPrompts(srv *server.MCPServer) {
	srv.AddPrompt(
		mcpgo.NewPrompt("deploy_app",
			mcpgo.WithPromptDescription("Walks through connecting to a device and deploying a project with the run tool."),
			mcpgo.WithArgument("project_path", mcpgo.ArgumentDescription("Path to the project to deploy (defaults to the current directory).")),
			mcpgo.WithArgument("device_name", mcpgo.ArgumentDescription("Cloud device name to target the run at (defaults to the currently connected/default device).")),
		),
		s.handleDeployAppPrompt,
	)

	srv.AddPrompt(
		mcpgo.NewPrompt("diagnose_container",
			mcpgo.WithPromptDescription("Walks through diagnosing a container that is failing, crash-looping, or misbehaving."),
			mcpgo.WithArgument("app_name", mcpgo.ArgumentDescription("Name of the app/container to diagnose (defaults to checking all containers).")),
		),
		s.handleDiagnoseContainerPrompt,
	)

	srv.AddPrompt(
		mcpgo.NewPrompt("provision_device",
			mcpgo.WithPromptDescription("Walks through connecting to an unprovisioned device and enrolling it with Wendy Cloud."),
			mcpgo.WithArgument("address", mcpgo.ArgumentDescription("Device address (host:port) to connect to (defaults to discovering one on the LAN).")),
		),
		s.handleProvisionDevicePrompt,
	)
}

// promptArg reads an optional prompt argument, returning defaultVal when the
// argument map is nil or the key is absent/empty.
func promptArg(req mcpgo.GetPromptRequest, name, defaultVal string) string {
	if req.Params.Arguments == nil {
		return defaultVal
	}
	if v, ok := req.Params.Arguments[name]; ok && v != "" {
		return v
	}
	return defaultVal
}

func (s *mcpServer) handleDeployAppPrompt(_ context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	projectPath := promptArg(req, "project_path", ".")
	device := promptArg(req, "device_name", "")

	deviceClause := "the currently connected device"
	if device != "" {
		deviceClause = fmt.Sprintf("device %q", device)
	}

	text := fmt.Sprintf(`Deploy the project at %s to %s.

1. Make sure a device is connected: use device_connect (for a LAN/direct device by host:port) or cloud_connect (for a cloud-enrolled device). If already connected, you can skip this.
2. Deploy with the run tool: run(project_path=%q%s). This builds the project and starts it on the device.
3. Verify it came up: check container_list for the app's running_state, and tail telemetry_logs for startup errors.
`, projectPath, deviceClause, projectPath, deviceArg(device))

	return mcpgo.NewGetPromptResult(
		"Deploy a project to a Wendy device",
		[]mcpgo.PromptMessage{
			mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(text)),
		},
	), nil
}

// deviceArg renders the optional device_name argument suffix for the run tool
// call shown in the deploy_app prompt text. The run tool's device selector is
// device_name (not device), so the rendered example must match that param.
func deviceArg(device string) string {
	if device == "" {
		return ""
	}
	return fmt.Sprintf(", device_name=%q", device)
}

func (s *mcpServer) handleDiagnoseContainerPrompt(_ context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	appName := promptArg(req, "app_name", "")

	target := "the container"
	listHint := "container_list"
	if appName != "" {
		target = fmt.Sprintf("%q", appName)
		listHint = fmt.Sprintf("container_list (filter for %q)", appName)
	}

	text := fmt.Sprintf(`Diagnose %s.

1. Call %s and inspect running_state and termination_reason. An error_code of ENTITLEMENT_DENIED means the container was denied a capability at start — fix the relevant permission in wendy.json and redeploy.
2. Call container_stats to check CPU/memory usage for signs of resource exhaustion or a crash loop.
3. Call telemetry_logs to read recent stdout/stderr output for the app-level error.
4. If the container is reachable but requests are failing, read the wendy://diagnostics resource — it records container-MCP proxy failures (app name, stage, error, time) that would otherwise only show up on stderr.
`, target, listHint)

	return mcpgo.NewGetPromptResult(
		"Diagnose a failing or misbehaving container",
		[]mcpgo.PromptMessage{
			mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(text)),
		},
	), nil
}

func (s *mcpServer) handleProvisionDevicePrompt(_ context.Context, req mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error) {
	address := promptArg(req, "address", "")

	connectHint := "device_connect (discover the address first with device_list scan=true if you don't have one)"
	if address != "" {
		connectHint = fmt.Sprintf("device_connect(address=%q)", address)
	}

	text := fmt.Sprintf(`Provision a device with Wendy Cloud.

1. Connect to the device: %s.
2. Check provisioning_status to see whether it is already provisioned or awaiting enrollment.
3. If unprovisioned, call provisioning_start with an enrollment_token (and cloud_host/organization_id as needed for your Wendy Cloud account) to begin enrollment.
4. Poll provisioning_status again to watch progress until it reports success (or an error you need to address, e.g. an expired token).
`, connectHint)

	return mcpgo.NewGetPromptResult(
		"Provision a device with Wendy Cloud",
		[]mcpgo.PromptMessage{
			mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(text)),
		},
	), nil
}
