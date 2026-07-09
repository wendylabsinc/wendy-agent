package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/simutil"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

// maxSimTaskEventLines bounds how many progress/log lines run_task_in_sim
// returns alongside the final result.
const maxSimTaskEventLines = 20

// maxSimContacts bounds how many contact pairs sim_contacts returns.
const maxSimContacts = 50

func (s *mcpServer) registerSimTools(srv *server.MCPServer) {
	srv.AddTool(mcpgo.NewTool("sim_create",
		mcpgo.WithDescription("Create a robot simulation world on the connected device. Returns the world_id and backend."),
		mcpgo.WithString("name",
			mcpgo.Required(),
			mcpgo.Description("Human-readable name for the simulation world"),
		),
		mcpgo.WithString("scene_yaml",
			mcpgo.Description("Optional scene description YAML forwarded to the sim backend (empty means a flat-floor world)"),
		),
	), s.handleSimCreate)

	srv.AddTool(mcpgo.NewTool("sim_list",
		mcpgo.WithDescription("List simulation worlds on the connected device, including the robots spawned in each"),
	), s.handleSimList)

	srv.AddTool(mcpgo.NewTool("sim_import_model",
		mcpgo.WithDescription("Import a robot model (MJCF) into the sim backend. Provide exactly one source: "+
			"menagerie_path for a bundled MuJoCo Menagerie model, or local_path for a model directory "+
			"or .tar/.tar.gz archive on this machine (it is streamed to the device). Returns the model_id."),
		mcpgo.WithString("name",
			mcpgo.Required(),
			mcpgo.Description("Registry name the model will be addressable by (e.g. \"go2\")"),
		),
		mcpgo.WithString("menagerie_path",
			mcpgo.Description("Bundled MuJoCo Menagerie model path (e.g. \"unitree_go2/go2.xml\")"),
		),
		mcpgo.WithString("local_path",
			mcpgo.Description("Local MJCF model directory or .tar/.tar.gz archive to upload"),
		),
	), s.handleSimImportModel)

	srv.AddTool(mcpgo.NewTool("sim_describe_model",
		mcpgo.WithDescription("Show an imported model's capability map: joints, actuators, sensors, cameras, "+
			"supported control levels, and safety limits. Use it to validate task specs before running them."),
		mcpgo.WithString("model_id",
			mcpgo.Required(),
			mcpgo.Description("Model ID returned by sim_import_model"),
		),
	), s.handleSimDescribeModel)

	srv.AddTool(mcpgo.NewTool("sim_spawn",
		mcpgo.WithDescription("Spawn an imported model into a simulation world. Returns the robot_id."),
		mcpgo.WithString("model_id",
			mcpgo.Required(),
			mcpgo.Description("Model ID returned by sim_import_model"),
		),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID returned by sim_create"),
		),
		mcpgo.WithString("pos",
			mcpgo.Description("Spawn position as \"x,y,z\" in meters (default 0,0,0)"),
		),
	), s.handleSimSpawn)

	srv.AddTool(mcpgo.NewTool("sim_state",
		mcpgo.WithDescription("Get a robot's current state: base pose, joint positions/velocities, sim time, and whether it has fallen"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
	), s.handleSimState)

	srv.AddTool(mcpgo.NewTool("sim_contacts",
		mcpgo.WithDescription("Get active contact pairs for a robot (body_a, body_b, force in Newtons); at most 50 are returned"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
	), s.handleSimContacts)

	srv.AddTool(mcpgo.NewTool("run_task_in_sim",
		mcpgo.WithDescription("Run a Wendy task spec against a robot in simulation and return the final TaskResult "+
			"(success, checks, fell, collisions, distance, replay_id) plus the last progress/log lines. "+
			"Provide the spec as inline YAML (spec_yaml) or a local file path (spec_path). "+
			"Task specs and their constraints/checks may be edited freely and re-run to iterate. "+
			"Changes to controller or application CODE must be proposed to the human for approval before running. "+
			"When record is true, download the recording afterwards with sim_replay using the returned replay_id."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID to run the task in"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID to drive"),
		),
		mcpgo.WithString("spec_yaml",
			mcpgo.Description("Task spec YAML, inline (mutually exclusive with spec_path)"),
		),
		mcpgo.WithString("spec_path",
			mcpgo.Description("Path to a task spec YAML file on this machine (mutually exclusive with spec_yaml)"),
		),
		mcpgo.WithBoolean("record",
			mcpgo.Description("Record a replay of the run (default true)"),
		),
		mcpgo.WithString("control_level",
			mcpgo.Description("Highest control level the task may use: task, motion, joint, or physics (default motion)"),
		),
	), s.handleRunTaskInSim)

	srv.AddTool(mcpgo.NewTool("sim_replay",
		mcpgo.WithDescription("Download a recorded replay (.rrd) to a local file and return the saved path and size"),
		mcpgo.WithString("replay_id",
			mcpgo.Required(),
			mcpgo.Description("Replay ID from a recorded run_task_in_sim result"),
		),
		mcpgo.WithString("output_path",
			mcpgo.Description("Where to save the replay (default ./replay-<replay-id>.rrd)"),
		),
	), s.handleSimReplay)

	srv.AddTool(mcpgo.NewTool("sim_reset",
		mcpgo.WithDescription("Reset a simulation world (and everything spawned in it) to its initial state"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID to reset"),
		),
	), s.handleSimReset)
}

func (s *mcpServer) handleSimCreate(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	name := stringParam(req, "name")
	if name == "" {
		return mcpgo.NewToolResultError("name is required"), nil
	}
	resp, err := conn.SimService.CreateSimulation(ctx, &agentpbv2.CreateSimulationRequest{
		Name:      name,
		SceneYaml: stringParam(req, "scene_yaml"),
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]string{
		"world_id": resp.GetWorldId(),
		"backend":  resp.GetBackend(),
	}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimList(ctx context.Context, _ mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	resp, err := conn.SimService.ListSimulations(ctx, &agentpbv2.ListSimulationsRequest{})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	sims := []map[string]any{}
	for _, sim := range resp.GetSimulations() {
		sims = append(sims, map[string]any{
			"world_id":  sim.GetWorldId(),
			"name":      sim.GetName(),
			"backend":   sim.GetBackend(),
			"robot_ids": sim.GetRobotIds(),
		})
	}
	b, _ := json.MarshalIndent(sims, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

// validateSimImportSource enforces exactly one model source for
// sim_import_model: a bundled Menagerie path or a local model path.
func validateSimImportSource(menageriePath, localPath string) error {
	switch {
	case menageriePath == "" && localPath == "":
		return fmt.Errorf("specify a model source: menagerie_path or local_path")
	case menageriePath != "" && localPath != "":
		return fmt.Errorf("menagerie_path and local_path are mutually exclusive")
	}
	return nil
}

func (s *mcpServer) handleSimImportModel(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	name := stringParam(req, "name")
	if name == "" {
		return mcpgo.NewToolResultError("name is required"), nil
	}
	menageriePath := stringParam(req, "menagerie_path")
	localPath := stringParam(req, "local_path")
	if err := validateSimImportSource(menageriePath, localPath); err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}

	stream, err := conn.SimService.ImportModel(ctx)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	if err := stream.Send(&simpb.LoadModelChunk{
		Payload: &simpb.LoadModelChunk_Source{Source: &simpb.ModelSource{
			Name:          name,
			Format:        simpb.ModelFormat_MODEL_FORMAT_MJCF,
			MenageriePath: menageriePath,
		}},
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}

	if localPath != "" {
		sendErr := simutil.StreamLocalModel(localPath, func(data []byte) error {
			return stream.Send(&simpb.LoadModelChunk{
				Payload: &simpb.LoadModelChunk_Data{Data: data},
			})
		})
		// A local read error aborts the import. Send returning io.EOF means
		// the stream broke; CloseAndRecv below surfaces the cause.
		if sendErr != nil && sendErr != io.EOF {
			return mcpgo.NewToolResultError(fmt.Sprintf("reading model %s: %v", localPath, sendErr)), nil
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]string{"model_id": resp.GetModelId()}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimDescribeModel(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	modelID := stringParam(req, "model_id")
	if modelID == "" {
		return mcpgo.NewToolResultError("model_id is required"), nil
	}
	resp, err := conn.SimService.DescribeModel(ctx, &simpb.DescribeModelRequest{ModelId: modelID})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	caps := resp.GetCapabilities()

	out := map[string]any{"model_id": modelID}
	joints := []map[string]any{}
	for _, j := range caps.GetJoints() {
		joints = append(joints, map[string]any{
			"name": j.GetName(), "type": j.GetType(),
			"range": []float64{j.GetRangeMin(), j.GetRangeMax()},
		})
	}
	out["joints"] = joints
	actuators := []map[string]any{}
	for _, a := range caps.GetActuators() {
		actuators = append(actuators, map[string]any{
			"name": a.GetName(), "joint": a.GetJoint(),
			"ctrl_range": []float64{a.GetCtrlMin(), a.GetCtrlMax()},
		})
	}
	out["actuators"] = actuators
	sensors := []string{}
	for _, sn := range caps.GetSensors() {
		sensors = append(sensors, fmt.Sprintf("%s (%s)", sn.GetName(), sn.GetType()))
	}
	out["sensors"] = sensors
	out["cameras"] = caps.GetCameras()
	levels := []string{}
	for _, l := range caps.GetSupportedControlLevels() {
		levels = append(levels, simutil.ControlLevelName(l))
	}
	out["control_levels"] = levels
	if sl := caps.GetSafetyLimits(); sl != nil {
		out["safety_limits"] = map[string]any{
			"max_linear_speed_mps":    sl.GetMaxLinearSpeedMps(),
			"max_angular_speed_radps": sl.GetMaxAngularSpeedRadps(),
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimSpawn(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	modelID := stringParam(req, "model_id")
	worldID := stringParam(req, "world_id")
	if modelID == "" || worldID == "" {
		return mcpgo.NewToolResultError("model_id and world_id are required"), nil
	}
	pose, err := simutil.ParsePosition(stringParam(req, "pos"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	resp, err := conn.SimService.SpawnRobot(ctx, &simpb.SpawnRequest{
		WorldId: worldID,
		ModelId: modelID,
		Pose:    pose,
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]string{"robot_id": resp.GetRobotId()}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimState(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	resp, err := conn.SimService.GetState(ctx, &simpb.GetStateRequest{
		WorldId: worldID,
		RobotId: robotID,
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	out := map[string]any{
		"sim_time_s": resp.GetSimTimeS(),
		"fallen":     resp.GetFallen(),
	}
	if p := resp.GetBasePose(); p != nil {
		out["base_pose"] = map[string]any{
			"position_m":       []float64{p.GetX(), p.GetY(), p.GetZ()},
			"orientation_wxyz": []float64{p.GetQw(), p.GetQx(), p.GetQy(), p.GetQz()},
		}
	}
	joints := []map[string]any{}
	for _, j := range resp.GetJoints() {
		joints = append(joints, map[string]any{
			"name":     j.GetName(),
			"position": j.GetPosition(),
			"velocity": j.GetVelocity(),
		})
	}
	out["joints"] = joints
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimContacts(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	resp, err := conn.SimService.GetContacts(ctx, &simpb.GetContactsRequest{
		WorldId: worldID,
		RobotId: robotID,
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	all := resp.GetContacts()
	contacts := []map[string]any{}
	for i, c := range all {
		if i >= maxSimContacts {
			break
		}
		contacts = append(contacts, map[string]any{
			"body_a":  c.GetBodyA(),
			"body_b":  c.GetBodyB(),
			"force_n": c.GetForceN(),
		})
	}
	out := map[string]any{
		"total_contacts": len(all),
		"contacts":       contacts,
	}
	if len(all) > maxSimContacts {
		out["truncated"] = true
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

// appendBoundedLine appends line, keeping only the last max entries.
func appendBoundedLine(lines []string, line string, max int) []string {
	lines = append(lines, line)
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

// simTaskEventLine renders a progress or log TaskEvent as one output line;
// ok is false for events that are not rendered (e.g. the final result).
func simTaskEventLine(ev *simpb.TaskEvent) (string, bool) {
	switch e := ev.GetEvent().(type) {
	case *simpb.TaskEvent_Progress:
		return fmt.Sprintf("[%7.2fs] %s", e.Progress.GetSimTimeS(), e.Progress.GetObjective()), true
	case *simpb.TaskEvent_Log:
		return "log: " + e.Log.GetMessage(), true
	default:
		return "", false
	}
}

// simTaskResultMap renders a TaskResult as the LLM-facing result object.
func simTaskResultMap(res *simpb.TaskResult) map[string]any {
	checks := []map[string]any{}
	for _, c := range res.GetChecks() {
		checks = append(checks, map[string]any{
			"name":   c.GetName(),
			"passed": c.GetPassed(),
			"detail": c.GetDetail(),
		})
	}
	return map[string]any{
		"success":             res.GetSuccess(),
		"fell":                res.GetFell(),
		"collision_count":     res.GetCollisionCount(),
		"distance_traveled_m": res.GetDistanceTraveledM(),
		"checks":              checks,
		"replay_id":           res.GetReplayId(),
		"summary":             res.GetSummary(),
	}
}

// resolveTaskSpec returns the task spec YAML from spec_yaml or spec_path
// (exactly one must be provided, and the spec must be non-empty).
func resolveTaskSpec(specYAML, specPath string) (string, error) {
	switch {
	case specYAML == "" && specPath == "":
		return "", fmt.Errorf("specify a task spec: spec_yaml (inline) or spec_path (local file)")
	case specYAML != "" && specPath != "":
		return "", fmt.Errorf("spec_yaml and spec_path are mutually exclusive")
	}
	if specPath != "" {
		data, err := os.ReadFile(specPath)
		if err != nil {
			return "", fmt.Errorf("reading task spec: %v", err)
		}
		specYAML = string(data)
	}
	if strings.TrimSpace(specYAML) == "" {
		return "", fmt.Errorf("task spec is empty")
	}
	return specYAML, nil
}

func (s *mcpServer) handleRunTaskInSim(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	specYAML, err := resolveTaskSpec(stringParam(req, "spec_yaml"), stringParam(req, "spec_path"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	controlLevel := stringParam(req, "control_level")
	if controlLevel == "" {
		controlLevel = "motion"
	}
	level, err := simutil.ParseControlLevel(controlLevel)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	record := req.GetBool("record", true)

	stream, err := conn.SimService.RunTask(ctx, &agentpbv2.RunSimTaskRequest{
		Task: &simpb.RunTaskRequest{
			WorldId:         worldID,
			RobotId:         robotID,
			SpecYaml:        specYAML,
			MaxControlLevel: level,
			Record:          record,
		},
		SessionControlLevel: level,
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}

	var result *simpb.TaskResult
	var events []string
	totalEvents := 0
	for {
		ev, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return mcpgo.NewToolResultError(grpcErrString(recvErr)), nil
		}
		if res := ev.GetResult(); res != nil {
			result = res
			continue
		}
		if line, ok := simTaskEventLine(ev); ok {
			events = appendBoundedLine(events, line, maxSimTaskEventLines)
			totalEvents++
		}
	}
	if result == nil {
		return mcpgo.NewToolResultError("task stream ended without a result"), nil
	}

	out := map[string]any{
		"result": simTaskResultMap(result),
		"events": events,
	}
	if totalEvents > len(events) {
		out["events_truncated"] = fmt.Sprintf("showing last %d of %d events", len(events), totalEvents)
	}
	if record && result.GetReplayId() != "" {
		out["replay_hint"] = fmt.Sprintf("download the recording with sim_replay(replay_id=%q)", result.GetReplayId())
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimReplay(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	replayID := stringParam(req, "replay_id")
	if replayID == "" {
		return mcpgo.NewToolResultError("replay_id is required"), nil
	}
	path := stringParam(req, "output_path")
	if path == "" {
		path = fmt.Sprintf("replay-%s.rrd", replayID)
	}
	n, err := simutil.DownloadReplay(ctx, conn.SimService, replayID, path)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]any{
		"replay_id": replayID,
		"path":      path,
		"bytes":     n,
	}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimReset(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	if worldID == "" {
		return mcpgo.NewToolResultError("world_id is required"), nil
	}
	if _, err := conn.SimService.ResetSimulation(ctx, &agentpbv2.ResetSimulationRequest{
		WorldId: worldID,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf("simulation %s reset", worldID)), nil
}
