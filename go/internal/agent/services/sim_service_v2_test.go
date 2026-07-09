package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

// fakeRobotBackend is an in-process simpb.RobotBackendServiceServer that
// records what the agent forwards to it.
type fakeRobotBackend struct {
	simpb.UnimplementedRobotBackendServiceServer

	mu           sync.Mutex
	worldCount   int
	spawnCount   int
	resetWorlds  []string
	loadedSource *simpb.ModelSource
	loadedData   []byte
	lastRunTask  *simpb.RunTaskRequest
	state        *simpb.GetStateResponse
}

func (f *fakeRobotBackend) CreateWorld(_ context.Context, req *simpb.CreateWorldRequest) (*simpb.CreateWorldResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.worldCount++
	return &simpb.CreateWorldResponse{WorldId: fmt.Sprintf("world-%d", f.worldCount)}, nil
}

func (f *fakeRobotBackend) Spawn(_ context.Context, req *simpb.SpawnRequest) (*simpb.SpawnResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawnCount++
	return &simpb.SpawnResponse{RobotId: fmt.Sprintf("robot-%d", f.spawnCount)}, nil
}

func (f *fakeRobotBackend) Reset(_ context.Context, req *simpb.ResetRequest) (*simpb.ResetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resetWorlds = append(f.resetWorlds, req.GetWorldId())
	return &simpb.ResetResponse{}, nil
}

func (f *fakeRobotBackend) GetState(_ context.Context, _ *simpb.GetStateRequest) (*simpb.GetStateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == nil {
		return nil, status.Error(codes.NotFound, "no state configured")
	}
	return f.state, nil
}

func (f *fakeRobotBackend) LoadModel(stream grpc.ClientStreamingServer[simpb.LoadModelChunk, simpb.LoadModelResponse]) error {
	var source *simpb.ModelSource
	var data bytes.Buffer
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
			source = p.Source
		case *simpb.LoadModelChunk_Data:
			data.Write(p.Data)
		}
	}
	if source == nil {
		return status.Error(codes.InvalidArgument, "first chunk must carry the model source")
	}
	f.mu.Lock()
	f.loadedSource = source
	f.loadedData = data.Bytes()
	f.mu.Unlock()
	return stream.SendAndClose(&simpb.LoadModelResponse{ModelId: "model-" + source.GetName()})
}

func (f *fakeRobotBackend) RunTask(req *simpb.RunTaskRequest, stream grpc.ServerStreamingServer[simpb.TaskEvent]) error {
	f.mu.Lock()
	f.lastRunTask = req
	f.mu.Unlock()
	if err := stream.Send(&simpb.TaskEvent{
		Event: &simpb.TaskEvent_Log{Log: &simpb.TaskLog{Message: "starting"}},
	}); err != nil {
		return err
	}
	return stream.Send(&simpb.TaskEvent{
		Event: &simpb.TaskEvent_Result{Result: &simpb.TaskResult{Success: true, Summary: "done"}},
	})
}

// startSimService wires a SimService (fronted by a real gRPC server) to an
// in-process fake backend via the dial seam and returns a client for it.
func startSimService(t *testing.T, backend simpb.RobotBackendServiceServer) agentpbv2.WendySimServiceClient {
	t.Helper()

	// Backend server on its own bufconn listener.
	backendLis := bufconn.Listen(bufSize)
	backendSrv := grpc.NewServer()
	simpb.RegisterRobotBackendServiceServer(backendSrv, backend)
	go func() { _ = backendSrv.Serve(backendLis) }()
	t.Cleanup(func() { backendSrv.Stop(); _ = backendLis.Close() })

	svc := NewSimService(zap.NewNop())
	svc.dial = func(addr string) (simpb.RobotBackendServiceClient, io.Closer, error) {
		conn, err := grpc.NewClient("passthrough:///sim-backend",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return backendLis.Dial() }),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, nil, err
		}
		return simpb.NewRobotBackendServiceClient(conn), conn, nil
	}
	t.Cleanup(func() { _ = svc.Close() })

	// Agent-facing server.
	agentLis := bufconn.Listen(bufSize)
	agentSrv := grpc.NewServer()
	agentpbv2.RegisterWendySimServiceServer(agentSrv, svc)
	go func() { _ = agentSrv.Serve(agentLis) }()
	conn, err := grpc.NewClient("passthrough:///sim-agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return agentLis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); agentSrv.Stop(); _ = agentLis.Close() })
	return agentpbv2.NewWendySimServiceClient(conn)
}

func TestSimService_CreateAndListSimulations(t *testing.T) {
	backend := &fakeRobotBackend{}
	client := startSimService(t, backend)
	ctx := context.Background()

	created, err := client.CreateSimulation(ctx, &agentpbv2.CreateSimulationRequest{Name: "flat-floor"})
	if err != nil {
		t.Fatalf("CreateSimulation: %v", err)
	}
	if created.GetWorldId() != "world-1" {
		t.Errorf("world_id = %q; want world-1", created.GetWorldId())
	}
	if created.GetBackend() != "mujoco" {
		t.Errorf("backend = %q; want mujoco", created.GetBackend())
	}

	if _, err := client.CreateSimulation(ctx, &agentpbv2.CreateSimulationRequest{Name: "obstacle-course"}); err != nil {
		t.Fatalf("CreateSimulation #2: %v", err)
	}

	// SpawnRobot should append the robot id to the world's bookkeeping.
	spawned, err := client.SpawnRobot(ctx, &simpb.SpawnRequest{WorldId: "world-1", ModelId: "model-go2"})
	if err != nil {
		t.Fatalf("SpawnRobot: %v", err)
	}
	if spawned.GetRobotId() != "robot-1" {
		t.Errorf("robot_id = %q; want robot-1", spawned.GetRobotId())
	}

	list, err := client.ListSimulations(ctx, &agentpbv2.ListSimulationsRequest{})
	if err != nil {
		t.Fatalf("ListSimulations: %v", err)
	}
	if len(list.GetSimulations()) != 2 {
		t.Fatalf("len(simulations) = %d; want 2", len(list.GetSimulations()))
	}
	first := list.GetSimulations()[0]
	if first.GetWorldId() != "world-1" || first.GetName() != "flat-floor" || first.GetBackend() != "mujoco" {
		t.Errorf("simulations[0] = %+v; want world-1/flat-floor/mujoco", first)
	}
	if len(first.GetRobotIds()) != 1 || first.GetRobotIds()[0] != "robot-1" {
		t.Errorf("simulations[0].robot_ids = %v; want [robot-1]", first.GetRobotIds())
	}
	if second := list.GetSimulations()[1]; len(second.GetRobotIds()) != 0 {
		t.Errorf("simulations[1].robot_ids = %v; want empty", second.GetRobotIds())
	}
}

func TestSimService_GetStatePassthrough(t *testing.T) {
	backend := &fakeRobotBackend{
		state: &simpb.GetStateResponse{
			BasePose: &simpb.Pose{X: 1.5, Y: -0.5, Z: 0.3, Qw: 1},
			Joints:   []*simpb.JointState{{Name: "FL_hip", Position: 0.12, Velocity: -0.4}},
			SimTimeS: 2.25,
			Fallen:   true,
		},
	}
	client := startSimService(t, backend)

	resp, err := client.GetState(context.Background(), &simpb.GetStateRequest{WorldId: "world-1", RobotId: "robot-1"})
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if resp.GetBasePose().GetX() != 1.5 || resp.GetBasePose().GetQw() != 1 {
		t.Errorf("base_pose = %+v; want x=1.5 qw=1", resp.GetBasePose())
	}
	if len(resp.GetJoints()) != 1 || resp.GetJoints()[0].GetName() != "FL_hip" {
		t.Errorf("joints = %+v; want one FL_hip joint", resp.GetJoints())
	}
	if !resp.GetFallen() {
		t.Error("fallen = false; want true")
	}
	if resp.GetSimTimeS() != 2.25 {
		t.Errorf("sim_time_s = %v; want 2.25", resp.GetSimTimeS())
	}
}

func TestSimService_ImportModelStreamPassthrough(t *testing.T) {
	backend := &fakeRobotBackend{}
	client := startSimService(t, backend)

	stream, err := client.ImportModel(context.Background())
	if err != nil {
		t.Fatalf("ImportModel: %v", err)
	}
	if err := stream.Send(&simpb.LoadModelChunk{
		Payload: &simpb.LoadModelChunk_Source{Source: &simpb.ModelSource{
			Name:   "go2",
			Format: simpb.ModelFormat_MODEL_FORMAT_MJCF,
		}},
	}); err != nil {
		t.Fatalf("Send(source): %v", err)
	}
	for _, part := range []string{"chunk-one|", "chunk-two"} {
		if err := stream.Send(&simpb.LoadModelChunk{
			Payload: &simpb.LoadModelChunk_Data{Data: []byte(part)},
		}); err != nil {
			t.Fatalf("Send(data): %v", err)
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
	if resp.GetModelId() != "model-go2" {
		t.Errorf("model_id = %q; want model-go2", resp.GetModelId())
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.loadedSource.GetName() != "go2" {
		t.Errorf("backend source name = %q; want go2", backend.loadedSource.GetName())
	}
	if backend.loadedSource.GetFormat() != simpb.ModelFormat_MODEL_FORMAT_MJCF {
		t.Errorf("backend source format = %v; want MJCF", backend.loadedSource.GetFormat())
	}
	if got := string(backend.loadedData); got != "chunk-one|chunk-two" {
		t.Errorf("backend data = %q; want chunk-one|chunk-two", got)
	}
}

func TestSimService_RunTaskClampsControlLevel(t *testing.T) {
	tests := []struct {
		name    string
		session simpb.ControlLevel
		task    simpb.ControlLevel
		want    simpb.ControlLevel
	}{
		{
			name:    "session lower than task clamps",
			session: simpb.ControlLevel_CONTROL_LEVEL_MOTION,
			task:    simpb.ControlLevel_CONTROL_LEVEL_JOINT,
			want:    simpb.ControlLevel_CONTROL_LEVEL_MOTION,
		},
		{
			name:    "session higher than task leaves task",
			session: simpb.ControlLevel_CONTROL_LEVEL_PHYSICS,
			task:    simpb.ControlLevel_CONTROL_LEVEL_MOTION,
			want:    simpb.ControlLevel_CONTROL_LEVEL_MOTION,
		},
		{
			name:    "unset session leaves task",
			session: simpb.ControlLevel_CONTROL_LEVEL_UNSPECIFIED,
			task:    simpb.ControlLevel_CONTROL_LEVEL_JOINT,
			want:    simpb.ControlLevel_CONTROL_LEVEL_JOINT,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeRobotBackend{}
			client := startSimService(t, backend)

			stream, err := client.RunTask(context.Background(), &agentpbv2.RunSimTaskRequest{
				Task: &simpb.RunTaskRequest{
					WorldId:         "world-1",
					RobotId:         "robot-1",
					SpecYaml:        "objectives: []",
					MaxControlLevel: tt.task,
				},
				SessionControlLevel: tt.session,
			})
			if err != nil {
				t.Fatalf("RunTask: %v", err)
			}

			var events []*simpb.TaskEvent
			for {
				ev, recvErr := stream.Recv()
				if recvErr == io.EOF {
					break
				}
				if recvErr != nil {
					t.Fatalf("Recv: %v", recvErr)
				}
				events = append(events, ev)
			}
			if len(events) != 2 {
				t.Fatalf("len(events) = %d; want 2", len(events))
			}
			if res := events[1].GetResult(); res == nil || !res.GetSuccess() {
				t.Errorf("events[1] = %+v; want successful result", events[1])
			}

			backend.mu.Lock()
			got := backend.lastRunTask.GetMaxControlLevel()
			backend.mu.Unlock()
			if got != tt.want {
				t.Errorf("forwarded max_control_level = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestSimService_RunTaskRequiresTask(t *testing.T) {
	client := startSimService(t, &fakeRobotBackend{})

	stream, err := client.RunTask(context.Background(), &agentpbv2.RunSimTaskRequest{})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("error code = %v; want InvalidArgument", status.Code(err))
	}
}

func TestSimService_GetReplayUnimplemented(t *testing.T) {
	client := startSimService(t, &fakeRobotBackend{})

	stream, err := client.GetReplay(context.Background(), &agentpbv2.GetReplayRequest{ReplayId: "r-1"})
	if err != nil {
		t.Fatalf("GetReplay: %v", err)
	}
	_, err = stream.Recv()
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("error code = %v; want Unimplemented", status.Code(err))
	}
}

func TestSimService_BackendDialFailureIsUnavailable(t *testing.T) {
	svc := NewSimService(zap.NewNop())
	svc.dial = func(addr string) (simpb.RobotBackendServiceClient, io.Closer, error) {
		return nil, nil, errors.New("boom")
	}

	_, err := svc.CreateSimulation(context.Background(), &agentpbv2.CreateSimulationRequest{Name: "w"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error code = %v; want Unavailable", status.Code(err))
	}
	if !strings.Contains(err.Error(), "WENDY_SIM_BACKEND_ADDR") {
		t.Errorf("error %q should mention WENDY_SIM_BACKEND_ADDR", err.Error())
	}
}

func TestSimService_BackendUnreachableIsUnavailable(t *testing.T) {
	svc := NewSimService(zap.NewNop())
	svc.dial = func(addr string) (simpb.RobotBackendServiceClient, io.Closer, error) {
		conn, err := grpc.NewClient("passthrough:///unreachable",
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
				return nil, errors.New("connection refused")
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			return nil, nil, err
		}
		return simpb.NewRobotBackendServiceClient(conn), conn, nil
	}
	t.Cleanup(func() { _ = svc.Close() })

	_, err := svc.CreateSimulation(context.Background(), &agentpbv2.CreateSimulationRequest{Name: "w"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("error code = %v; want Unavailable", status.Code(err))
	}
	if !strings.Contains(err.Error(), "WENDY_SIM_BACKEND_ADDR") {
		t.Errorf("error %q should mention WENDY_SIM_BACKEND_ADDR", err.Error())
	}
}
