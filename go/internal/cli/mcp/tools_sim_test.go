package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// fakeSimServer implements WendySimServiceServer for sim tool tests.
type fakeSimServer struct {
	agentpbv2.UnimplementedWendySimServiceServer

	simulations []*agentpbv2.SimulationInfo
	state       *simpb.GetStateResponse
	contacts    []*simpb.Contact
	replayData  [][]byte

	// RunTask behavior: emit progressEvents progress events, then the result.
	progressEvents int
	taskResult     *simpb.TaskResult

	// Captured by ImportModel / RunTask / ResetSimulation.
	importedSource *simpb.ModelSource
	importedBytes  int
	lastRunTask    *agentpbv2.RunSimTaskRequest
	resetWorldID   string

	// Interactive tooling captures / fixtures.
	lastSetClock   *simpb.SetClockRequest
	lastApplyForce *simpb.ApplyForceRequest
	lastTeleport   *simpb.TeleportRequest
	lastRestore    *simpb.RestoreSnapshotRequest
	sensorReadings []*simpb.SensorReading
	lastEditScene  *simpb.EditSceneRequest
	policySource   *simpb.PolicySource
	policyBytes    int
	clearedPolicy  *simpb.ClearPolicyRequest
	videoChunks    [][]byte
	lastRender     *simpb.RenderVideoRequest
}

func (f *fakeSimServer) SetClock(_ context.Context, req *simpb.SetClockRequest) (*simpb.SetClockResponse, error) {
	f.lastSetClock = req
	return &simpb.SetClockResponse{Paused: req.GetPaused(), SpeedFactor: req.GetSpeedFactor()}, nil
}

func (f *fakeSimServer) ApplyForce(_ context.Context, req *simpb.ApplyForceRequest) (*simpb.ApplyForceResponse, error) {
	f.lastApplyForce = req
	return &simpb.ApplyForceResponse{}, nil
}

func (f *fakeSimServer) Teleport(_ context.Context, req *simpb.TeleportRequest) (*simpb.TeleportResponse, error) {
	f.lastTeleport = req
	return &simpb.TeleportResponse{}, nil
}

func (f *fakeSimServer) SaveSnapshot(_ context.Context, _ *simpb.SaveSnapshotRequest) (*simpb.SaveSnapshotResponse, error) {
	return &simpb.SaveSnapshotResponse{SnapshotId: "snap-1"}, nil
}

func (f *fakeSimServer) RestoreSnapshot(_ context.Context, req *simpb.RestoreSnapshotRequest) (*simpb.RestoreSnapshotResponse, error) {
	f.lastRestore = req
	return &simpb.RestoreSnapshotResponse{}, nil
}

func (f *fakeSimServer) ReadSensors(_ context.Context, _ *simpb.ReadSensorsRequest) (*simpb.ReadSensorsResponse, error) {
	return &simpb.ReadSensorsResponse{Readings: f.sensorReadings}, nil
}

func (f *fakeSimServer) EditScene(_ context.Context, req *simpb.EditSceneRequest) (*simpb.EditSceneResponse, error) {
	f.lastEditScene = req
	return &simpb.EditSceneResponse{}, nil
}

func (f *fakeSimServer) LoadPolicy(stream grpc.ClientStreamingServer[simpb.LoadPolicyChunk, simpb.LoadPolicyResponse]) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch p := chunk.GetPayload().(type) {
		case *simpb.LoadPolicyChunk_Source:
			f.policySource = p.Source
		case *simpb.LoadPolicyChunk_Data:
			f.policyBytes += len(p.Data)
		}
	}
	return stream.SendAndClose(&simpb.LoadPolicyResponse{PolicyId: "policy-1"})
}

func (f *fakeSimServer) ClearPolicy(_ context.Context, req *simpb.ClearPolicyRequest) (*simpb.ClearPolicyResponse, error) {
	f.clearedPolicy = req
	return &simpb.ClearPolicyResponse{}, nil
}

func (f *fakeSimServer) RenderVideo(req *simpb.RenderVideoRequest, stream grpc.ServerStreamingServer[simpb.VideoChunk]) error {
	f.lastRender = req
	for _, data := range f.videoChunks {
		if err := stream.Send(&simpb.VideoChunk{Data: data}); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSimServer) CreateSimulation(_ context.Context, req *agentpbv2.CreateSimulationRequest) (*agentpbv2.CreateSimulationResponse, error) {
	return &agentpbv2.CreateSimulationResponse{WorldId: "world-" + req.GetName(), Backend: "mujoco"}, nil
}

func (f *fakeSimServer) ListSimulations(_ context.Context, _ *agentpbv2.ListSimulationsRequest) (*agentpbv2.ListSimulationsResponse, error) {
	return &agentpbv2.ListSimulationsResponse{Simulations: f.simulations}, nil
}

func (f *fakeSimServer) ResetSimulation(_ context.Context, req *agentpbv2.ResetSimulationRequest) (*agentpbv2.ResetSimulationResponse, error) {
	f.resetWorldID = req.GetWorldId()
	return &agentpbv2.ResetSimulationResponse{}, nil
}

func (f *fakeSimServer) ImportModel(stream grpc.ClientStreamingServer[simpb.LoadModelChunk, simpb.LoadModelResponse]) error {
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch p := chunk.GetPayload().(type) {
		case *simpb.LoadModelChunk_Source:
			f.importedSource = p.Source
		case *simpb.LoadModelChunk_Data:
			f.importedBytes += len(p.Data)
		}
	}
	return stream.SendAndClose(&simpb.LoadModelResponse{ModelId: "model-1"})
}

func (f *fakeSimServer) DescribeModel(_ context.Context, _ *simpb.DescribeModelRequest) (*simpb.DescribeModelResponse, error) {
	return &simpb.DescribeModelResponse{Capabilities: &simpb.CapabilityMap{
		Joints: []*simpb.JointInfo{
			{Name: "FL_hip", Type: "hinge", RangeMin: -1, RangeMax: 1},
		},
		Actuators: []*simpb.ActuatorInfo{
			{Name: "FL_hip_act", Joint: "FL_hip", CtrlMin: -20, CtrlMax: 20},
		},
		Sensors: []*simpb.SensorInfo{{Name: "imu", Type: "gyro"}},
		Cameras: []string{"tracking"},
		SupportedControlLevels: []simpb.ControlLevel{
			simpb.ControlLevel_CONTROL_LEVEL_TASK,
			simpb.ControlLevel_CONTROL_LEVEL_MOTION,
		},
		SafetyLimits: &simpb.SafetyLimits{MaxLinearSpeedMps: 1.5, MaxAngularSpeedRadps: 2},
	}}, nil
}

func (f *fakeSimServer) SpawnRobot(_ context.Context, req *simpb.SpawnRequest) (*simpb.SpawnResponse, error) {
	return &simpb.SpawnResponse{RobotId: "robot-1"}, nil
}

func (f *fakeSimServer) GetState(_ context.Context, _ *simpb.GetStateRequest) (*simpb.GetStateResponse, error) {
	return f.state, nil
}

func (f *fakeSimServer) GetContacts(_ context.Context, _ *simpb.GetContactsRequest) (*simpb.GetContactsResponse, error) {
	return &simpb.GetContactsResponse{Contacts: f.contacts}, nil
}

func (f *fakeSimServer) RunTask(req *agentpbv2.RunSimTaskRequest, stream grpc.ServerStreamingServer[simpb.TaskEvent]) error {
	f.lastRunTask = req
	for i := 0; i < f.progressEvents; i++ {
		if err := stream.Send(&simpb.TaskEvent{Event: &simpb.TaskEvent_Progress{
			Progress: &simpb.TaskProgress{Objective: fmt.Sprintf("step-%d", i), SimTimeS: float64(i)},
		}}); err != nil {
			return err
		}
	}
	if f.taskResult != nil {
		return stream.Send(&simpb.TaskEvent{Event: &simpb.TaskEvent_Result{Result: f.taskResult}})
	}
	return nil
}

func (f *fakeSimServer) GetReplay(_ *agentpbv2.GetReplayRequest, stream grpc.ServerStreamingServer[agentpbv2.GetReplayChunk]) error {
	for _, data := range f.replayData {
		if err := stream.Send(&agentpbv2.GetReplayChunk{Data: data}); err != nil {
			return err
		}
	}
	return nil
}

func startFakeSimServer(t *testing.T, fake *fakeSimServer) *grpcclient.AgentConnection {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	g := grpc.NewServer()
	agentpbv2.RegisterWendySimServiceServer(g, fake)
	go func() { _ = g.Serve(ln) }()
	t.Cleanup(func() { g.Stop() })

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &grpcclient.AgentConnection{
		Conn:       conn,
		SimService: agentpbv2.NewWendySimServiceClient(conn),
	}
}

func TestSimTools_NotConnected(t *testing.T) {
	srv := New(&config.Config{}, nil)
	for _, tool := range []string{
		"sim_create", "sim_list", "sim_import_model", "sim_describe_model",
		"sim_spawn", "sim_state", "sim_contacts", "run_task_in_sim",
		"sim_replay", "sim_reset", "sim_clock", "sim_push", "sim_teleport",
		"sim_snapshot_save", "sim_snapshot_restore", "sim_sensors",
		"sim_scene_edit", "sim_policy_load", "sim_policy_clear", "sim_record",
	} {
		result, err := srv.callTool(context.Background(), tool, map[string]any{})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tool, err)
		}
		if !result.IsError {
			t.Errorf("%s: expected IsError=true when not connected", tool)
		}
	}
}

func TestSimCreate_ReturnsWorldIDAndBackend(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_create", map[string]any{"name": "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["world_id"] != "world-test" || got["backend"] != "mujoco" {
		t.Errorf("sim_create = %v; want world-test/mujoco", got)
	}
}

func TestSimCreate_RequiresName(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_create", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true without name")
	}
}

func TestSimList_ReturnsSimulationsWithRobots(t *testing.T) {
	fake := &fakeSimServer{
		simulations: []*agentpbv2.SimulationInfo{
			{WorldId: "w1", Name: "test", Backend: "mujoco", RobotIds: []string{"r1", "r2"}},
		},
	}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_list", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var sims []map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &sims); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(sims) != 1 || sims[0]["world_id"] != "w1" {
		t.Fatalf("sims = %v; want one entry w1", sims)
	}
	robots, _ := sims[0]["robot_ids"].([]any)
	if len(robots) != 2 {
		t.Errorf("robot_ids = %v; want 2 entries", sims[0]["robot_ids"])
	}
}

func TestValidateSimImportSource(t *testing.T) {
	tests := []struct {
		name      string
		menagerie string
		local     string
		wantErr   bool
	}{
		{name: "menagerie only", menagerie: "unitree_go2/go2.xml"},
		{name: "local only", local: "./model"},
		{name: "neither", wantErr: true},
		{name: "both", menagerie: "unitree_go2/go2.xml", local: "./model", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSimImportSource(tt.menagerie, tt.local)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSimImportSource(%q, %q) = %v; wantErr %v",
					tt.menagerie, tt.local, err, tt.wantErr)
			}
		})
	}
}

func TestSimImportModel_Menagerie(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_import_model", map[string]any{
		"name":           "go2",
		"menagerie_path": "unitree_go2/go2.xml",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["model_id"] != "model-1" {
		t.Errorf("model_id = %q; want model-1", got["model_id"])
	}
	if fake.importedSource.GetName() != "go2" || fake.importedSource.GetMenageriePath() != "unitree_go2/go2.xml" {
		t.Errorf("imported source = %+v", fake.importedSource)
	}
	if fake.importedBytes != 0 {
		t.Errorf("menagerie import streamed %d data bytes; want 0", fake.importedBytes)
	}
}

func TestSimImportModel_LocalDirectoryStreamsTar(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "robot.xml"), []byte("<mujoco/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_import_model", map[string]any{
		"name":       "custom",
		"local_path": dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if fake.importedBytes == 0 {
		t.Error("local import streamed no data bytes; want a tar archive")
	}
}

func TestSimImportModel_SourceValidation(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	for name, args := range map[string]map[string]any{
		"neither": {"name": "go2"},
		"both":    {"name": "go2", "menagerie_path": "a/b.xml", "local_path": "./model"},
		"no name": {"menagerie_path": "a/b.xml"},
	} {
		result, err := srv.callTool(context.Background(), "sim_import_model", args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !result.IsError {
			t.Errorf("%s: expected IsError=true", name)
		}
	}
}

func TestSimDescribeModel_ReturnsCapabilities(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_describe_model", map[string]any{"model_id": "model-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	levels, _ := got["control_levels"].([]any)
	if len(levels) != 2 || levels[0] != "task" || levels[1] != "motion" {
		t.Errorf("control_levels = %v; want [task motion]", got["control_levels"])
	}
	joints, _ := got["joints"].([]any)
	if len(joints) != 1 {
		t.Errorf("joints = %v; want 1 entry", got["joints"])
	}
	if got["safety_limits"] == nil {
		t.Error("missing safety_limits")
	}
}

func TestSimSpawn_ReturnsRobotID(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_spawn", map[string]any{
		"model_id": "model-1",
		"world_id": "w1",
		"pos":      "1,2,0.5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["robot_id"] != "robot-1" {
		t.Errorf("robot_id = %q; want robot-1", got["robot_id"])
	}
}

func TestSimSpawn_InvalidPos(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_spawn", map[string]any{
		"model_id": "model-1",
		"world_id": "w1",
		"pos":      "1,2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for malformed pos")
	}
}

func TestSimState_ReturnsPoseJointsAndFallen(t *testing.T) {
	fake := &fakeSimServer{
		state: &simpb.GetStateResponse{
			BasePose: &simpb.Pose{X: 1, Y: 2, Z: 0.3, Qw: 1},
			Joints: []*simpb.JointState{
				{Name: "FL_hip", Position: 0.1, Velocity: -0.2},
			},
			SimTimeS: 4.2,
			Fallen:   true,
		},
	}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_state", map[string]any{
		"world_id": "w1", "robot_id": "r1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["fallen"] != true || got["sim_time_s"] != 4.2 {
		t.Errorf("state = %v; want fallen=true sim_time_s=4.2", got)
	}
	if got["base_pose"] == nil {
		t.Error("missing base_pose")
	}
	joints, _ := got["joints"].([]any)
	if len(joints) != 1 {
		t.Errorf("joints = %v; want 1 entry", got["joints"])
	}
}

func TestSimContacts_CapsAtFifty(t *testing.T) {
	fake := &fakeSimServer{}
	for i := 0; i < 60; i++ {
		fake.contacts = append(fake.contacts, &simpb.Contact{
			BodyA: fmt.Sprintf("body-%d", i), BodyB: "floor", ForceN: 1,
		})
	}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_contacts", map[string]any{
		"world_id": "w1", "robot_id": "r1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	contacts, _ := got["contacts"].([]any)
	if len(contacts) != maxSimContacts {
		t.Errorf("len(contacts) = %d; want %d", len(contacts), maxSimContacts)
	}
	if got["total_contacts"] != float64(60) || got["truncated"] != true {
		t.Errorf("total_contacts/truncated = %v/%v; want 60/true", got["total_contacts"], got["truncated"])
	}
}

func TestResolveTaskSpec(t *testing.T) {
	specFile := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(specFile, []byte("objective: walk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(t.TempDir(), "empty.yaml")
	if err := os.WriteFile(emptyFile, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		specYAML string
		specPath string
		want     string
		wantErr  bool
	}{
		{name: "inline", specYAML: "objective: walk\n", want: "objective: walk\n"},
		{name: "file", specPath: specFile, want: "objective: walk\n"},
		{name: "neither", wantErr: true},
		{name: "both", specYAML: "x", specPath: specFile, wantErr: true},
		{name: "missing file", specPath: filepath.Join(t.TempDir(), "nope.yaml"), wantErr: true},
		{name: "empty inline", specYAML: "   ", wantErr: true},
		{name: "empty file", specPath: emptyFile, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveTaskSpec(tt.specYAML, tt.specPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveTaskSpec error = %v; wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("resolveTaskSpec = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestAppendBoundedLine(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = appendBoundedLine(lines, fmt.Sprintf("line-%d", i), 20)
	}
	if len(lines) != 20 {
		t.Fatalf("len(lines) = %d; want 20", len(lines))
	}
	if lines[0] != "line-5" || lines[19] != "line-24" {
		t.Errorf("lines[0], lines[19] = %q, %q; want line-5, line-24", lines[0], lines[19])
	}
}

func TestSimTaskEventLine(t *testing.T) {
	progress := &simpb.TaskEvent{Event: &simpb.TaskEvent_Progress{
		Progress: &simpb.TaskProgress{Objective: "move_forward", SimTimeS: 1.5},
	}}
	line, ok := simTaskEventLine(progress)
	if !ok || !strings.Contains(line, "move_forward") || !strings.Contains(line, "1.50s") {
		t.Errorf("progress line = %q, ok = %v", line, ok)
	}

	logEv := &simpb.TaskEvent{Event: &simpb.TaskEvent_Log{Log: &simpb.TaskLog{Message: "spawned"}}}
	line, ok = simTaskEventLine(logEv)
	if !ok || line != "log: spawned" {
		t.Errorf("log line = %q, ok = %v", line, ok)
	}

	result := &simpb.TaskEvent{Event: &simpb.TaskEvent_Result{Result: &simpb.TaskResult{}}}
	if _, ok := simTaskEventLine(result); ok {
		t.Error("result events should not render as lines")
	}
}

func TestRunTaskInSim_SpecValidation(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	for name, args := range map[string]map[string]any{
		"neither": {"world_id": "w1", "robot_id": "r1"},
		"both": {"world_id": "w1", "robot_id": "r1",
			"spec_yaml": "x", "spec_path": "task.yaml"},
		"bad control level": {"world_id": "w1", "robot_id": "r1",
			"spec_yaml": "objective: walk", "control_level": "torque"},
		"missing ids": {"spec_yaml": "objective: walk"},
	} {
		result, err := srv.callTool(context.Background(), "run_task_in_sim", args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !result.IsError {
			t.Errorf("%s: expected IsError=true", name)
		}
	}
}

func TestRunTaskInSim_ReturnsResultAndBoundedEvents(t *testing.T) {
	fake := &fakeSimServer{
		progressEvents: 30,
		taskResult: &simpb.TaskResult{
			Success:           true,
			DistanceTraveledM: 2.5,
			Checks: []*simpb.CheckResult{
				{Name: "not_fallen", Passed: true},
			},
			ReplayId: "replay-42",
			Summary:  "walked 2.5 m",
		},
	}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "run_task_in_sim", map[string]any{
		"world_id":  "w1",
		"robot_id":  "r1",
		"spec_yaml": "objective: walk\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got struct {
		Result struct {
			Success           bool    `json:"success"`
			DistanceTraveledM float64 `json:"distance_traveled_m"`
			ReplayID          string  `json:"replay_id"`
			Summary           string  `json:"summary"`
			Checks            []struct {
				Name   string `json:"name"`
				Passed bool   `json:"passed"`
			} `json:"checks"`
		} `json:"result"`
		Events          []string `json:"events"`
		EventsTruncated string   `json:"events_truncated"`
		ReplayHint      string   `json:"replay_hint"`
	}
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !got.Result.Success || got.Result.DistanceTraveledM != 2.5 ||
		got.Result.ReplayID != "replay-42" || got.Result.Summary != "walked 2.5 m" {
		t.Errorf("result = %+v", got.Result)
	}
	if len(got.Result.Checks) != 1 || got.Result.Checks[0].Name != "not_fallen" || !got.Result.Checks[0].Passed {
		t.Errorf("checks = %+v", got.Result.Checks)
	}
	if len(got.Events) != maxSimTaskEventLines {
		t.Errorf("len(events) = %d; want %d", len(got.Events), maxSimTaskEventLines)
	}
	// The bounded buffer keeps the LAST events.
	if !strings.Contains(got.Events[len(got.Events)-1], "step-29") {
		t.Errorf("last event = %q; want step-29", got.Events[len(got.Events)-1])
	}
	if !strings.Contains(got.EventsTruncated, "20 of 30") {
		t.Errorf("events_truncated = %q; want mention of 20 of 30", got.EventsTruncated)
	}
	if !strings.Contains(got.ReplayHint, "replay-42") {
		t.Errorf("replay_hint = %q; want mention of replay-42", got.ReplayHint)
	}

	// The request forwarded the defaults: record=true, motion control level.
	if !fake.lastRunTask.GetTask().GetRecord() {
		t.Error("record should default to true")
	}
	if fake.lastRunTask.GetTask().GetMaxControlLevel() != simpb.ControlLevel_CONTROL_LEVEL_MOTION ||
		fake.lastRunTask.GetSessionControlLevel() != simpb.ControlLevel_CONTROL_LEVEL_MOTION {
		t.Errorf("control levels = %v/%v; want motion/motion",
			fake.lastRunTask.GetTask().GetMaxControlLevel(), fake.lastRunTask.GetSessionControlLevel())
	}
}

func TestRunTaskInSim_NoResultIsError(t *testing.T) {
	fake := &fakeSimServer{progressEvents: 2} // stream ends without a result
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "run_task_in_sim", map[string]any{
		"world_id": "w1", "robot_id": "r1", "spec_yaml": "objective: walk\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true when the stream ends without a result")
	}
}

func TestSimReplay_WritesFileAndReturnsPath(t *testing.T) {
	fake := &fakeSimServer{replayData: [][]byte{[]byte("rrd|"), []byte("data")}}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	path := filepath.Join(t.TempDir(), "out.rrd")
	result, err := srv.callTool(context.Background(), "sim_replay", map[string]any{
		"replay_id":   "replay-42",
		"output_path": path,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["path"] != path || got["bytes"] != float64(8) || got["replay_id"] != "replay-42" {
		t.Errorf("sim_replay = %v; want path/bytes=8/replay-42", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "rrd|data" {
		t.Errorf("file content = %q; want rrd|data", data)
	}
}

func TestSimReset_ForwardsWorldID(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_reset", map[string]any{"world_id": "w1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if text := toolResultText(t, result); text != "simulation w1 reset" {
		t.Errorf("text = %q; want %q", text, "simulation w1 reset")
	}
	if fake.resetWorldID != "w1" {
		t.Errorf("resetWorldID = %q; want w1", fake.resetWorldID)
	}
}

func TestSimClock_ForwardsAndReturnsState(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_clock", map[string]any{
		"world_id": "w1", "paused": true, "speed_factor": 4.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["paused"] != true || got["speed_factor"] != 4.0 {
		t.Errorf("sim_clock = %v; want paused=true speed_factor=4", got)
	}
	if fake.lastSetClock.GetWorldId() != "w1" || !fake.lastSetClock.GetPaused() ||
		fake.lastSetClock.GetSpeedFactor() != 4 {
		t.Errorf("forwarded request = %+v", fake.lastSetClock)
	}
}

func TestSimPush_ForwardsForce(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_push", map[string]any{
		"world_id": "w1", "robot_id": "r1", "force": "30,0,-5", "duration_s": 0.25,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	got := fake.lastApplyForce
	if got.GetFxN() != 30 || got.GetFyN() != 0 || got.GetFzN() != -5 || got.GetDurationS() != 0.25 {
		t.Errorf("forwarded request = %+v", got)
	}
}

func TestSimPush_DefaultsDurationAndValidatesForce(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	if result, err := srv.callTool(context.Background(), "sim_push", map[string]any{
		"world_id": "w1", "robot_id": "r1", "force": "30,0,0",
	}); err != nil || result.IsError {
		t.Fatalf("push without duration: err=%v result=%v", err, result)
	}
	if fake.lastApplyForce.GetDurationS() != 0.1 {
		t.Errorf("default duration = %v; want 0.1", fake.lastApplyForce.GetDurationS())
	}

	for name, force := range map[string]string{"missing": "", "malformed": "1,2"} {
		result, err := srv.callTool(context.Background(), "sim_push", map[string]any{
			"world_id": "w1", "robot_id": "r1", "force": force,
		})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !result.IsError {
			t.Errorf("%s force: expected IsError=true", name)
		}
	}
}

func TestSimTeleport_ForwardsPoseWithZeroVelocity(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_teleport", map[string]any{
		"world_id": "w1", "robot_id": "r1", "pos": "1,2,0.5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	got := fake.lastTeleport
	if got.GetPose().GetX() != 1 || got.GetPose().GetY() != 2 || got.GetPose().GetZ() != 0.5 {
		t.Errorf("forwarded pose = %+v", got.GetPose())
	}
	if !got.GetZeroVelocity() {
		t.Error("zero_velocity should be true")
	}

	missing, err := srv.callTool(context.Background(), "sim_teleport", map[string]any{
		"world_id": "w1", "robot_id": "r1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !missing.IsError {
		t.Error("teleport without pos: expected IsError=true")
	}
}

func TestSimSnapshotSaveAndRestore(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_snapshot_save", map[string]any{"world_id": "w1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["snapshot_id"] != "snap-1" {
		t.Errorf("snapshot_id = %q; want snap-1", got["snapshot_id"])
	}

	restore, err := srv.callTool(context.Background(), "sim_snapshot_restore", map[string]any{
		"world_id": "w1", "snapshot_id": "snap-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restore.IsError {
		t.Fatalf("unexpected error result: %v", restore.Content)
	}
	if fake.lastRestore.GetWorldId() != "w1" || fake.lastRestore.GetSnapshotId() != "snap-1" {
		t.Errorf("forwarded restore = %+v", fake.lastRestore)
	}
}

func TestSimSensors_ReturnsBoundedReadings(t *testing.T) {
	fake := &fakeSimServer{}
	for i := 0; i < 60; i++ {
		fake.sensorReadings = append(fake.sensorReadings, &simpb.SensorReading{
			Name: fmt.Sprintf("sensor-%d", i), Type: "touch", Values: []float64{1},
		})
	}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_sensors", map[string]any{
		"world_id": "w1", "robot_id": "r1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	readings, _ := got["readings"].([]any)
	if len(readings) != maxSimSensorReadings {
		t.Errorf("len(readings) = %d; want %d", len(readings), maxSimSensorReadings)
	}
	if got["total_sensors"] != float64(60) || got["truncated"] != true {
		t.Errorf("total/truncated = %v/%v; want 60/true", got["total_sensors"], got["truncated"])
	}
}

func TestSimSceneEdit_AddBoxAndRemove(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_scene_edit", map[string]any{
		"world_id": "w1", "op": "add_box", "id": "crate", "pos": "1,0,0.25", "size": "0.5,0.5,0.5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	box := fake.lastEditScene.GetAddBox()
	if box.GetId() != "crate" || box.GetPosition()[0] != 1 || box.GetSize()[2] != 0.5 {
		t.Errorf("forwarded add_box = %+v", box)
	}

	remove, err := srv.callTool(context.Background(), "sim_scene_edit", map[string]any{
		"world_id": "w1", "op": "remove", "id": "crate",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if remove.IsError {
		t.Fatalf("unexpected error result: %v", remove.Content)
	}
	if fake.lastEditScene.GetRemoveId() != "crate" {
		t.Errorf("forwarded remove_id = %q; want crate", fake.lastEditScene.GetRemoveId())
	}
}

func TestSimSceneEdit_Validation(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	for name, args := range map[string]map[string]any{
		"bad op":           {"world_id": "w1", "op": "add_sphere", "id": "s1"},
		"add_box no pos":   {"world_id": "w1", "op": "add_box", "id": "b1", "size": "1,1,1"},
		"add_box bad size": {"world_id": "w1", "op": "add_box", "id": "b1", "pos": "0,0,0", "size": "1,1"},
		"missing id":       {"world_id": "w1", "op": "remove"},
	} {
		result, err := srv.callTool(context.Background(), "sim_scene_edit", args)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !result.IsError {
			t.Errorf("%s: expected IsError=true", name)
		}
	}
}

func TestSimPolicyLoad_StreamsFile(t *testing.T) {
	policy := filepath.Join(t.TempDir(), "policy.onnx")
	if err := os.WriteFile(policy, []byte("onnx-model-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_policy_load", map[string]any{
		"world_id": "w1", "robot_id": "r1", "local_path": policy,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["policy_id"] != "policy-1" {
		t.Errorf("policy_id = %q; want policy-1", got["policy_id"])
	}
	if fake.policySource.GetWorldId() != "w1" || fake.policySource.GetRobotId() != "r1" ||
		fake.policySource.GetFormat() != "onnx" {
		t.Errorf("policy source = %+v", fake.policySource)
	}
	if fake.policyBytes != len("onnx-model-bytes") {
		t.Errorf("policy bytes = %d; want %d", fake.policyBytes, len("onnx-model-bytes"))
	}
}

func TestSimPolicyLoad_MissingFile(t *testing.T) {
	conn := startFakeSimServer(t, &fakeSimServer{})
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_policy_load", map[string]any{
		"world_id": "w1", "robot_id": "r1",
		"local_path": filepath.Join(t.TempDir(), "nope.onnx"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected IsError=true for a missing policy file")
	}
}

func TestSimPolicyClear_Forwards(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_policy_clear", map[string]any{
		"world_id": "w1", "robot_id": "r1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if fake.clearedPolicy.GetWorldId() != "w1" || fake.clearedPolicy.GetRobotId() != "r1" {
		t.Errorf("forwarded request = %+v", fake.clearedPolicy)
	}
}

func TestSimRecord_WritesFileAndReturnsPathNotBytes(t *testing.T) {
	fake := &fakeSimServer{videoChunks: [][]byte{[]byte("mp4|"), []byte("data")}}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	path := filepath.Join(t.TempDir(), "clip.mp4")
	result, err := srv.callTool(context.Background(), "sim_record", map[string]any{
		"world_id": "w1", "robot_id": "r1", "duration_s": 2.0, "fps": 30, "output_path": path,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	text := toolResultText(t, result)
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if got["path"] != path || got["bytes"] != float64(8) {
		t.Errorf("sim_record = %v; want path/bytes=8", got)
	}
	if strings.Contains(text, "mp4|data") {
		t.Error("sim_record must not return the video bytes")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "mp4|data" {
		t.Errorf("file content = %q; want mp4|data", data)
	}
	if fake.lastRender.GetDurationS() != 2 || fake.lastRender.GetFps() != 30 {
		t.Errorf("forwarded request = %+v", fake.lastRender)
	}
}

func TestSimImportModel_ReplaceModelID(t *testing.T) {
	fake := &fakeSimServer{}
	conn := startFakeSimServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)

	result, err := srv.callTool(context.Background(), "sim_import_model", map[string]any{
		"name":             "go2",
		"menagerie_path":   "unitree_go2/go2.xml",
		"replace_model_id": "model-old",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if fake.importedSource.GetReplaceModelId() != "model-old" {
		t.Errorf("replace_model_id = %q; want model-old", fake.importedSource.GetReplaceModelId())
	}
}
