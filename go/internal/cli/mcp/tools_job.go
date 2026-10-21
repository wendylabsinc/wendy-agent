package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/jobops"
)

func (s *mcpServer) registerJobTools(srv *server.MCPServer) {
	opts := []mcpgo.ToolOption{mcpgo.WithDescription("Read an existing host training job: bounded progress, process identity, freshness, errors and full-report reference. Requires authenticated Linux host access, python3 and status.json in job_path. Never changes a job. Use since and wait_seconds to avoid repeated full snapshots."), mcpgo.WithString("job_path", mcpgo.Required(), mcpgo.Description("Absolute directory containing status.json and optional child metrics.jsonl files")), mcpgo.WithString("since", mcpgo.Description("Cursor from a previous result")), mcpgo.WithInteger("wait_seconds", mcpgo.Min(0), mcpgo.Max(30), mcpgo.Description("Wait for a changed snapshot; requires since. Omitted: 0."))}
	opts = append(opts, readOnly()...)
	opts = append(opts, openWorld()...)
	srv.AddTool(mcpgo.NewTool("job_inspect", opts...), s.handleJobInspect)
	submit := []mcpgo.ToolOption{mcpgo.WithDescription("Start a named durable host job from a bounded step specification. Stores metadata/logs under working_directory/.wendy/jobs/name and runs requested argv with host-agent permissions. Records exits/timeouts and optionally verifies source hashes before launch. Workload commands may write elsewhere. Identical name/spec retries reuse the original job; a different spec never restarts it. This executes workloads: choose the target and runtime explicitly. Receipt means started, not successful."), mcpgo.WithObject("spec", mcpgo.Required(), mcpgo.Properties(map[string]any{
		"name":              map[string]any{"type": "string", "description": "Unique job name; lowercase letters, digits, underscore or hyphen"},
		"working_directory": map[string]any{"type": "string", "description": "Absolute existing remote source directory"},
		"steps":             map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": map[string]any{"type": "object", "required": []string{"name", "argv", "timeout_seconds"}, "additionalProperties": false, "properties": map[string]any{"name": map[string]any{"type": "string"}, "argv": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}, "description": "Executable and args, no shell expansion. {job_dir} and {step_dir} expand to owned output paths."}, "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 86400}}}},
		"source_hashes":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Optional relative source/checkpoint paths mapped to expected SHA-256"},
	}))}
	submit = append(submit, mutating()...)
	submit = append(submit, idempotent()...)
	submit = append(submit, openWorld()...)
	submitTool := mcpgo.NewTool("job_submit", submit...)
	submitSchema := submitTool.InputSchema.Properties["spec"].(map[string]any)
	submitSchema["required"] = []string{"name", "working_directory", "steps"}
	submitSchema["additionalProperties"] = false
	srv.AddTool(submitTool, s.handleJobSubmit)

	compute := []mcpgo.ToolOption{mcpgo.WithDescription("Read current GPU identity, utilization, compute processes and CPU/memory/disk headroom in one bounded snapshot. Missing GPU tools stay unknown. Does not reserve the device, start training or change jobs. Requires authenticated Linux host access and python3.")}
	compute = append(compute, readOnly()...)
	compute = append(compute, openWorld()...)
	srv.AddTool(mcpgo.NewTool("compute_status", compute...), func(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		conn, _, target := s.connectionSnapshot()
		if conn == nil {
			return errNotConnected(), nil
		}
		out, err := jobops.ComputeStatus(ctx, conn)
		if err != nil {
			return errResult(errCodeInternal, err.Error()), nil
		}
		out["target"] = target.Selector
		return jobJSON(out), nil
	})
	pull := []mcpgo.ToolOption{mcpgo.WithDescription("Pull selected host-job report snapshots to a local directory. Verifies hashes, skips unchanged files and returns a compact manifest reference; file bytes never enter tool output. Reads remote files and may replace selected local files. Maximum 64 files and 16 MiB transferred per call."), mcpgo.WithString("job_path", mcpgo.Required(), mcpgo.Description("Absolute remote job directory")), mcpgo.WithArray("files", mcpgo.Required(), mcpgo.WithStringItems(), mcpgo.Description("Selected relative files, for example status.json or control/metrics.jsonl")), mcpgo.WithString("destination", mcpgo.Required(), mcpgo.Description("Absolute local output directory"))}
	pull = append(pull, mutating()...)
	pull = append(pull, idempotent()...)
	pull = append(pull, openWorld()...)
	srv.AddTool(mcpgo.NewTool("job_artifacts_pull", pull...), s.handleJobArtifactsPull)
}
func (s *mcpServer) handleJobInspect(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	jobPath, ok := req.GetArguments()["job_path"].(string)
	if !ok || jobPath == "" {
		return errResult(errCodeInvalidArgument, "job_path must be an absolute job directory"), nil
	}
	since := ""
	if v, exists := req.GetArguments()["since"]; exists {
		var valid bool
		since, valid = v.(string)
		if !valid {
			return errResult(errCodeInvalidArgument, "since must be a string"), nil
		}
	}
	wait, err := ros2Int(req, "wait_seconds", 0, 0, 30)
	if err != nil {
		return errResult(errCodeInvalidArgument, err.Error()), nil
	}
	if err = jobops.ValidateInspect(jobPath, jobops.InspectOptions{Since: since, WaitSeconds: wait}); err != nil {
		return errResult(errCodeInvalidArgument, err.Error()), nil
	}
	conn, _, target := s.connectionSnapshot()
	if conn == nil {
		return errNotConnected(), nil
	}
	result, err := jobops.Inspect(ctx, conn, jobPath, jobops.InspectOptions{Since: since, WaitSeconds: wait, Target: target.Selector})
	if err != nil {
		return errResult(errCodeInternal, err.Error()), nil
	}
	selector := target.Selector
	if selector == "" {
		selector = conn.Addr
	}
	result["target"] = selector
	return jobJSON(result), nil
}

func (s *mcpServer) handleJobArtifactsPull(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	jobPath, ok := req.GetArguments()["job_path"].(string)
	if !ok {
		return errResult(errCodeInvalidArgument, "job_path must be an absolute directory"), nil
	}
	destination, ok := req.GetArguments()["destination"].(string)
	if !ok || !filepath.IsAbs(destination) {
		return errResult(errCodeInvalidArgument, "destination must be an absolute local directory"), nil
	}
	raw, ok := req.GetArguments()["files"]
	if !ok {
		return errResult(errCodeInvalidArgument, "files is required"), nil
	}
	encoded, err := json.Marshal(raw)
	var files []string
	if err != nil || json.Unmarshal(encoded, &files) != nil || len(files) == 0 {
		return errResult(errCodeInvalidArgument, "files must be a nonempty array of relative paths"), nil
	}
	conn, _, target := s.connectionSnapshot()
	if conn == nil {
		return errNotConnected(), nil
	}
	result, err := jobops.Pull(ctx, conn, jobPath, destination, files)
	if err != nil {
		return errResult(errCodeInternal, err.Error()), nil
	}
	result["target"] = target.Selector
	return jobJSON(result), nil
}

// New workflow tools emit one JSON representation. Existing tools retain their
// compatibility text plus structuredContent; duplicating these new snapshots
// would needlessly repeat observations in clients that expose both to a model.
func jobJSON(value map[string]any) *mcpgo.CallToolResult {
	data, err := json.Marshal(value)
	if err != nil {
		return errResult(errCodeInternal, "cannot encode job result")
	}
	return okText(string(data))
}

func (s *mcpServer) handleJobSubmit(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	raw, exists := req.GetArguments()["spec"]
	if !exists {
		return errResult(errCodeInvalidArgument, "spec is required"), nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return errResult(errCodeInvalidArgument, "invalid spec"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var spec jobops.Spec
	if err = decoder.Decode(&spec); err != nil {
		return errResult(errCodeInvalidArgument, err.Error()), nil
	}
	if err = jobops.ValidateSpec(spec); err != nil {
		return errResult(errCodeInvalidArgument, err.Error()), nil
	}
	conn, _, target := s.connectionSnapshot()
	if conn == nil {
		return errNotConnected(), nil
	}
	receipt, err := jobops.Submit(ctx, conn, spec)
	if err != nil {
		return errResult(errCodeInternal, err.Error()), nil
	}
	receipt["target"] = target.Selector
	return jobJSON(receipt), nil
}
