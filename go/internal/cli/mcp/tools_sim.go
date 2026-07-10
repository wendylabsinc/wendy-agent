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

// maxSimSensorReadings bounds how many readings sim_sensors returns.
const maxSimSensorReadings = 50

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
		mcpgo.WithDescription("Import a robot model into the sim backend. Provide exactly one source: "+
			"menagerie_path for a bundled MuJoCo Menagerie model, or local_path for a model directory "+
			"or .tar/.tar.gz archive on this machine (it is streamed to the device). Format is "+
			"auto-detected (mjcf/sdf/urdf; override with format) — which formats load depends on the "+
			"backend (MuJoCo: mjcf; Gazebo: sdf, urdf). Set replace_model_id to reload an existing "+
			"model in place (robots spawned from it are respawned — the edit-reload loop). "+
			"Returns the model_id."),
		mcpgo.WithString("name",
			mcpgo.Required(),
			mcpgo.Description("Registry name the model will be addressable by (e.g. \"go2\")"),
		),
		mcpgo.WithString("menagerie_path",
			mcpgo.Description("Bundled MuJoCo Menagerie model path (e.g. \"unitree_go2/go2.xml\")"),
		),
		mcpgo.WithString("format",
			mcpgo.Description("Model format: mjcf, sdf, or urdf (default: auto-detect from the source)"),
		),
		mcpgo.WithString("local_path",
			mcpgo.Description("Local model directory or .tar/.tar.gz archive to upload"),
		),
		mcpgo.WithString("replace_model_id",
			mcpgo.Description("Existing model ID to replace in place (robots spawned from it are respawned)"),
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

	srv.AddTool(mcpgo.NewTool("sim_drive",
		mcpgo.WithDescription("Command a base velocity for a simulated robot (motion level). The robot holds "+
			"the command until the next sim_drive or a task takes over; the backend clamps to the model's "+
			"safety limits. Omit all velocities (or send zeros) to stop. Poll sim_state to see the effect."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
		mcpgo.WithNumber("vx",
			mcpgo.Description("Forward velocity in m/s (negative = backward)"),
		),
		mcpgo.WithNumber("vy",
			mcpgo.Description("Lateral velocity in m/s"),
		),
		mcpgo.WithNumber("yaw_rate",
			mcpgo.Description("Turn rate in rad/s (positive = counter-clockwise)"),
		),
	), s.handleSimDrive)

	srv.AddTool(mcpgo.NewTool("sim_viewer_url",
		mcpgo.WithDescription("Get the URL of the sim's live browser viewer (served when the sim container "+
			"runs with WENDYSIM_LIVE_VIEWER=1). Share it with the human so they can watch the simulation live."),
		mcpgo.WithString("host",
			mcpgo.Description("Sim host (default: the connected device's host)"),
		),
	), s.handleSimViewerURL)

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
			"When record is true, download the recording afterwards with sim_replay using the returned replay_id. "+
			"Spec schema — objective: list of {move_forward: {distance_m}} or {wait: {seconds}}; "+
			"constraints: mapping (max_speed_mps, do_not_fall, avoid_collisions); "+
			"checks: list of not_fallen, {distance_traveled: {min_m}}, {collision_count: {max}}. Example:\n"+
			"objective:\n  - move_forward: {distance_m: 3.0}\nconstraints:\n  max_speed_mps: 0.5\n  do_not_fall: true\n"+
			"checks:\n  - not_fallen\n  - distance_traveled: {min_m: 2.5}\n  - collision_count: {max: 0}"),
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

	srv.AddTool(mcpgo.NewTool("sim_clock",
		mcpgo.WithDescription("Pause/resume a simulation world and set its real-time pacing. "+
			"paused=true freezes physics (advance deterministically with run_task_in_sim after resuming, "+
			"or resume with paused=false); speed_factor scales pacing (1 = real time, 10 = 10x, "+
			"0 = leave unchanged). Returns the resulting clock state."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithBoolean("paused",
			mcpgo.Description("true pauses physics stepping; false (default) resumes it"),
		),
		mcpgo.WithNumber("speed_factor",
			mcpgo.Description("Real-time pacing multiplier (1 = real time, 10 = 10x; 0/omitted = unchanged)"),
		),
	), s.handleSimClock)

	srv.AddTool(mcpgo.NewTool("sim_push",
		mcpgo.WithDescription("Apply a world-frame force impulse to a robot's base — the programmatic "+
			"shove, used to test balance and recovery. Check the effect with sim_state."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
		mcpgo.WithString("force",
			mcpgo.Required(),
			mcpgo.Description("World-frame force as \"x,y,z\" in Newtons (e.g. \"30,0,0\")"),
		),
		mcpgo.WithNumber("duration_s",
			mcpgo.Description("How long the force is held, in sim seconds (default 0.1; 0 = one physics step)"),
		),
	), s.handleSimPush)

	srv.AddTool(mcpgo.NewTool("sim_teleport",
		mcpgo.WithDescription("Move a robot to a position directly (physics-level edit); its velocities are zeroed"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
		mcpgo.WithString("pos",
			mcpgo.Required(),
			mcpgo.Description("Target position as \"x,y,z\" in meters"),
		),
	), s.handleSimTeleport)

	srv.AddTool(mcpgo.NewTool("sim_snapshot_save",
		mcpgo.WithDescription("Capture a world's exact physics state; restore it later with "+
			"sim_snapshot_restore to reproduce a scenario deterministically. Returns the snapshot_id."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
	), s.handleSimSnapshotSave)

	srv.AddTool(mcpgo.NewTool("sim_snapshot_restore",
		mcpgo.WithDescription("Rewind a world to a snapshot saved with sim_snapshot_save"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("snapshot_id",
			mcpgo.Required(),
			mcpgo.Description("Snapshot ID returned by sim_snapshot_save"),
		),
	), s.handleSimSnapshotRestore)

	srv.AddTool(mcpgo.NewTool("sim_sensors",
		mcpgo.WithDescription("Read current values of a robot's declared sensors (IMU, force, touch, "+
			"rangefinder, ...); at most 50 readings are returned"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
	), s.handleSimSensors)

	srv.AddTool(mcpgo.NewTool("sim_scene_edit",
		mcpgo.WithDescription("Edit a live world's static scenery without recreating it. "+
			"op=add_box adds a static box obstacle (requires id, pos, size); "+
			"op=remove removes an obstacle by id."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("op",
			mcpgo.Required(),
			mcpgo.Description("Scene operation: add_box or remove"),
		),
		mcpgo.WithString("id",
			mcpgo.Required(),
			mcpgo.Description("Obstacle ID (names a new box, or selects the one to remove)"),
		),
		mcpgo.WithString("pos",
			mcpgo.Description("Box center as \"x,y,z\" in meters (add_box only)"),
		),
		mcpgo.WithString("size",
			mcpgo.Description("Box full extents as \"x,y,z\" in meters (add_box only)"),
		),
	), s.handleSimSceneEdit)

	srv.AddTool(mcpgo.NewTool("sim_policy_load",
		mcpgo.WithDescription("Load a trained policy file (e.g. ONNX) from this machine onto a simulated "+
			"robot, replacing its built-in controller. Revert with sim_policy_clear. Returns the policy_id."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
		mcpgo.WithString("local_path",
			mcpgo.Required(),
			mcpgo.Description("Local policy file to upload (e.g. policy.onnx)"),
		),
		mcpgo.WithString("format",
			mcpgo.Description("Policy file format (default onnx)"),
		),
	), s.handleSimPolicyLoad)

	srv.AddTool(mcpgo.NewTool("sim_policy_clear",
		mcpgo.WithDescription("Revert a robot to its built-in controller (undoes sim_policy_load)"),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID returned by sim_spawn"),
		),
	), s.handleSimPolicyClear)

	srv.AddTool(mcpgo.NewTool("sim_record",
		mcpgo.WithDescription("Render a video clip (mp4) of the simulation and save it to a local file. "+
			"Returns the saved path and size — share the path with the human; do not try to read the bytes."),
		mcpgo.WithString("world_id",
			mcpgo.Required(),
			mcpgo.Description("World ID"),
		),
		mcpgo.WithString("robot_id",
			mcpgo.Required(),
			mcpgo.Description("Robot ID the tracking camera follows"),
		),
		mcpgo.WithNumber("duration_s",
			mcpgo.Description("Clip length in sim seconds (default 5)"),
		),
		mcpgo.WithNumber("fps",
			mcpgo.Description("Frames per second (default 15)"),
		),
		mcpgo.WithString("camera",
			mcpgo.Description("Model camera name (default: tracking view)"),
		),
		mcpgo.WithString("output_path",
			mcpgo.Description("Where to save the clip (default ./clip-<world-id>.mp4)"),
		),
	), s.handleSimRecord)
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
	format, err := simutil.ResolveModelFormat(stringParam(req, "format"), menageriePath, localPath)
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}

	stream, err := conn.SimService.ImportModel(ctx)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	if err := stream.Send(&simpb.LoadModelChunk{
		Payload: &simpb.LoadModelChunk_Source{Source: &simpb.ModelSource{
			Name:           name,
			Format:         format,
			MenageriePath:  menageriePath,
			ReplaceModelId: stringParam(req, "replace_model_id"),
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

func (s *mcpServer) handleSimDrive(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	vx := req.GetFloat("vx", 0)
	vy := req.GetFloat("vy", 0)
	yawRate := req.GetFloat("yaw_rate", 0)

	if _, err := conn.SimService.SetVelocity(ctx, &simpb.SetVelocityRequest{
		WorldId:      worldID,
		RobotId:      robotID,
		VxMps:        vx,
		VyMps:        vy,
		YawRateRadps: yawRate,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	if vx == 0 && vy == 0 && yawRate == 0 {
		return mcpgo.NewToolResultText(fmt.Sprintf("robot %s stopping", robotID)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf(
		"driving %s: vx=%.2f m/s, vy=%.2f m/s, yaw_rate=%.2f rad/s (holds until the next sim_drive)",
		robotID, vx, vy, yawRate)), nil
}

func (s *mcpServer) handleSimViewerURL(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	host := stringParam(req, "host")
	if host == "" {
		conn := s.GetConn()
		if conn == nil {
			return errNotConnected(), nil
		}
		host = conn.Host
	}
	if host == "" {
		host = "localhost"
	}
	url := fmt.Sprintf("http://%s:9877/?url=rerun%%2Bhttp://%s:9876/proxy", host, host)
	return mcpgo.NewToolResultText(fmt.Sprintf(
		"live viewer: %s (requires the sim container to run with WENDYSIM_LIVE_VIEWER=1)", url)), nil
}

func (s *mcpServer) handleSimClock(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	if worldID == "" {
		return mcpgo.NewToolResultError("world_id is required"), nil
	}
	resp, err := conn.SimService.SetClock(ctx, &simpb.SetClockRequest{
		WorldId:     worldID,
		Paused:      req.GetBool("paused", false),
		SpeedFactor: req.GetFloat("speed_factor", 0),
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]any{
		"paused":       resp.GetPaused(),
		"speed_factor": resp.GetSpeedFactor(),
	}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimPush(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	force, err := simutil.ParseVector3(stringParam(req, "force"), "force")
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	duration := req.GetFloat("duration_s", 0.1)

	if _, err := conn.SimService.ApplyForce(ctx, &simpb.ApplyForceRequest{
		WorldId:   worldID,
		RobotId:   robotID,
		FxN:       force[0],
		FyN:       force[1],
		FzN:       force[2],
		DurationS: duration,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf(
		"pushed %s with force (%.1f, %.1f, %.1f) N for %.2f s; check sim_state for the effect",
		robotID, force[0], force[1], force[2], duration)), nil
}

func (s *mcpServer) handleSimTeleport(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	pose, err := simutil.ParsePosition(stringParam(req, "pos"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
	if pose == nil {
		return mcpgo.NewToolResultError("pos is required (\"x,y,z\" in meters)"), nil
	}

	if _, err := conn.SimService.Teleport(ctx, &simpb.TeleportRequest{
		WorldId:      worldID,
		RobotId:      robotID,
		Pose:         pose,
		ZeroVelocity: true,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf("teleported %s to (%.3f, %.3f, %.3f); velocities zeroed",
		robotID, pose.GetX(), pose.GetY(), pose.GetZ())), nil
}

func (s *mcpServer) handleSimSnapshotSave(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	if worldID == "" {
		return mcpgo.NewToolResultError("world_id is required"), nil
	}
	resp, err := conn.SimService.SaveSnapshot(ctx, &simpb.SaveSnapshotRequest{WorldId: worldID})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]string{"snapshot_id": resp.GetSnapshotId()}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimSnapshotRestore(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	snapshotID := stringParam(req, "snapshot_id")
	if worldID == "" || snapshotID == "" {
		return mcpgo.NewToolResultError("world_id and snapshot_id are required"), nil
	}
	if _, err := conn.SimService.RestoreSnapshot(ctx, &simpb.RestoreSnapshotRequest{
		WorldId:    worldID,
		SnapshotId: snapshotID,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf("world %s restored to snapshot %s", worldID, snapshotID)), nil
}

func (s *mcpServer) handleSimSensors(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	resp, err := conn.SimService.ReadSensors(ctx, &simpb.ReadSensorsRequest{
		WorldId: worldID,
		RobotId: robotID,
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	all := resp.GetReadings()
	readings := []map[string]any{}
	for i, r := range all {
		if i >= maxSimSensorReadings {
			break
		}
		readings = append(readings, map[string]any{
			"name":   r.GetName(),
			"type":   r.GetType(),
			"values": r.GetValues(),
		})
	}
	out := map[string]any{
		"total_sensors": len(all),
		"readings":      readings,
	}
	if len(all) > maxSimSensorReadings {
		out["truncated"] = true
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimSceneEdit(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	id := stringParam(req, "id")
	if worldID == "" || id == "" {
		return mcpgo.NewToolResultError("world_id and id are required"), nil
	}

	edit := &simpb.EditSceneRequest{WorldId: worldID}
	var summary string
	switch op := stringParam(req, "op"); op {
	case "add_box":
		pos, err := simutil.ParseVector3(stringParam(req, "pos"), "pos")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		size, err := simutil.ParseVector3(stringParam(req, "size"), "size")
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		edit.Op = &simpb.EditSceneRequest_AddBox{AddBox: &simpb.SceneBoxSpec{
			Id:       id,
			Position: pos[:],
			Size:     size[:],
		}}
		summary = fmt.Sprintf("box %s added at (%.2f, %.2f, %.2f) with size (%.2f, %.2f, %.2f)",
			id, pos[0], pos[1], pos[2], size[0], size[1], size[2])
	case "remove":
		edit.Op = &simpb.EditSceneRequest_RemoveId{RemoveId: id}
		summary = fmt.Sprintf("obstacle %s removed", id)
	default:
		return mcpgo.NewToolResultError(fmt.Sprintf("invalid op %q: expected add_box or remove", op)), nil
	}

	if _, err := conn.SimService.EditScene(ctx, edit); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(summary), nil
}

func (s *mcpServer) handleSimPolicyLoad(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	localPath := stringParam(req, "local_path")
	if worldID == "" || robotID == "" || localPath == "" {
		return mcpgo.NewToolResultError("world_id, robot_id, and local_path are required"), nil
	}
	format := stringParam(req, "format")
	if format == "" {
		format = "onnx"
	}
	if _, err := os.Stat(localPath); err != nil {
		return mcpgo.NewToolResultError(fmt.Sprintf("opening policy file: %v", err)), nil
	}

	stream, err := conn.SimService.LoadPolicy(ctx)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	if err := stream.Send(&simpb.LoadPolicyChunk{
		Payload: &simpb.LoadPolicyChunk_Source{Source: &simpb.PolicySource{
			WorldId: worldID,
			RobotId: robotID,
			Format:  format,
		}},
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}

	sendErr := simutil.StreamFileChunks(localPath, func(data []byte) error {
		return stream.Send(&simpb.LoadPolicyChunk{
			Payload: &simpb.LoadPolicyChunk_Data{Data: data},
		})
	})
	// A local read error aborts the load. Send returning io.EOF means the
	// stream broke; CloseAndRecv below surfaces the cause.
	if sendErr != nil && sendErr != io.EOF {
		return mcpgo.NewToolResultError(fmt.Sprintf("reading policy %s: %v", localPath, sendErr)), nil
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]string{"policy_id": resp.GetPolicyId()}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}

func (s *mcpServer) handleSimPolicyClear(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}
	if _, err := conn.SimService.ClearPolicy(ctx, &simpb.ClearPolicyRequest{
		WorldId: worldID,
		RobotId: robotID,
	}); err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	return mcpgo.NewToolResultText(fmt.Sprintf("policy cleared; %s uses its built-in controller", robotID)), nil
}

func (s *mcpServer) handleSimRecord(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	worldID := stringParam(req, "world_id")
	robotID := stringParam(req, "robot_id")
	if worldID == "" || robotID == "" {
		return mcpgo.NewToolResultError("world_id and robot_id are required"), nil
	}

	stream, err := conn.SimService.RenderVideo(ctx, &simpb.RenderVideoRequest{
		WorldId:    worldID,
		RobotId:    robotID,
		CameraName: stringParam(req, "camera"),
		DurationS:  req.GetFloat("duration_s", 5),
		Fps:        uint32(req.GetInt("fps", 15)),
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}

	path := stringParam(req, "output_path")
	if path == "" {
		path = fmt.Sprintf("clip-%s.mp4", worldID)
	}
	n, err := simutil.WriteReplayFile(path, func() ([]byte, error) {
		chunk, recvErr := stream.Recv()
		if recvErr != nil {
			return nil, recvErr
		}
		return chunk.GetData(), nil
	})
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.MarshalIndent(map[string]any{
		"path":  path,
		"bytes": n,
	}, "", "  ")
	return mcpgo.NewToolResultText(string(b)), nil
}
