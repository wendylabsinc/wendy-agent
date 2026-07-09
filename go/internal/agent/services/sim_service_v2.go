package services

import (
	"context"
	"io"
	"os"
	"sort"
	"sync"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"github.com/wendylabsinc/wendy/go/proto/gen/simpb"
)

const (
	// simBackendAddrEnv names the environment variable that points the agent
	// at a running RobotBackendService (plaintext gRPC).
	simBackendAddrEnv = "WENDY_SIM_BACKEND_ADDR"
	// defaultSimBackendAddr is used when WENDY_SIM_BACKEND_ADDR is unset.
	defaultSimBackendAddr = "localhost:9090"
	// simBackendKey identifies the backend implementation. P1 supports a
	// single MuJoCo backend; multi-backend selection (WENDY_SIM_BACKEND)
	// comes later.
	simBackendKey = "mujoco"
)

// SimService fronts a simulation backend (wendy.sim.v1.RobotBackendService,
// running as a containerized service — MuJoCo first) toward the CLI/MCP. It
// lazily dials the backend at WENDY_SIM_BACKEND_ADDR and forwards
// observation/control/task calls, clamping task control levels to the
// caller's session ControlLevel.
type SimService struct {
	agentpbv2.UnimplementedWendySimServiceServer
	logger *zap.Logger

	// dial is the backend dial seam (overridden in tests).
	dial func(addr string) (simpb.RobotBackendServiceClient, io.Closer, error)

	mu          sync.Mutex
	backend     simpb.RobotBackendServiceClient
	backendConn io.Closer
	backendAddr string
	worlds      map[string]*simWorld
}

// simWorld is the in-memory bookkeeping for a created world; the backend owns
// the authoritative state.
type simWorld struct {
	name     string
	backend  string
	robotIDs []string
}

// NewSimService builds the sim service. The backend is not dialed until the
// first RPC needs it.
func NewSimService(logger *zap.Logger) *SimService {
	return &SimService{
		logger: logger,
		dial:   dialSimBackend,
		worlds: make(map[string]*simWorld),
	}
}

// dialSimBackend opens a plaintext gRPC client to the backend. grpc.NewClient
// is lazy, so reachability problems surface as codes.Unavailable on the first
// forwarded RPC rather than here.
func dialSimBackend(addr string) (simpb.RobotBackendServiceClient, io.Closer, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	return simpb.NewRobotBackendServiceClient(conn), conn, nil
}

// client returns the backend client, dialing it on first use.
func (s *SimService) client() (simpb.RobotBackendServiceClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backend != nil {
		return s.backend, nil
	}
	addr := os.Getenv(simBackendAddrEnv)
	if addr == "" {
		addr = defaultSimBackendAddr
	}
	client, conn, err := s.dial(addr)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable,
			"sim backend at %s unreachable (configure via %s): %v", addr, simBackendAddrEnv, err)
	}
	s.backend = client
	s.backendConn = conn
	s.backendAddr = addr
	return client, nil
}

// Close releases the backend connection, if one was dialed.
func (s *SimService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backendConn == nil {
		return nil
	}
	err := s.backendConn.Close()
	s.backend = nil
	s.backendConn = nil
	return err
}

// backendErr translates transport-level failures into an actionable message;
// backend application errors pass through unchanged.
func (s *SimService) backendErr(err error) error {
	if status.Code(err) != codes.Unavailable {
		return err
	}
	s.mu.Lock()
	addr := s.backendAddr
	s.mu.Unlock()
	if addr == "" {
		addr = defaultSimBackendAddr
	}
	return status.Errorf(codes.Unavailable,
		"sim backend at %s unreachable (configure via %s): %v", addr, simBackendAddrEnv, err)
}

// --- Simulation lifecycle ---

func (s *SimService) CreateSimulation(ctx context.Context, req *agentpbv2.CreateSimulationRequest) (*agentpbv2.CreateSimulationResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.CreateWorld(ctx, &simpb.CreateWorldRequest{
		Name:      req.GetName(),
		SceneYaml: req.GetSceneYaml(),
	})
	if err != nil {
		return nil, s.backendErr(err)
	}

	s.mu.Lock()
	s.worlds[resp.GetWorldId()] = &simWorld{name: req.GetName(), backend: simBackendKey}
	s.mu.Unlock()

	if s.logger != nil {
		s.logger.Info("sim: world created",
			zap.String("world_id", resp.GetWorldId()), zap.String("name", req.GetName()))
	}
	return &agentpbv2.CreateSimulationResponse{
		WorldId: resp.GetWorldId(),
		Backend: simBackendKey,
	}, nil
}

func (s *SimService) ListSimulations(_ context.Context, _ *agentpbv2.ListSimulationsRequest) (*agentpbv2.ListSimulationsResponse, error) {
	s.mu.Lock()
	sims := make([]*agentpbv2.SimulationInfo, 0, len(s.worlds))
	for id, w := range s.worlds {
		sims = append(sims, &agentpbv2.SimulationInfo{
			WorldId:  id,
			Name:     w.name,
			Backend:  w.backend,
			RobotIds: append([]string(nil), w.robotIDs...),
		})
	}
	s.mu.Unlock()

	sort.Slice(sims, func(i, j int) bool { return sims[i].WorldId < sims[j].WorldId })
	return &agentpbv2.ListSimulationsResponse{Simulations: sims}, nil
}

func (s *SimService) ResetSimulation(ctx context.Context, req *agentpbv2.ResetSimulationRequest) (*agentpbv2.ResetSimulationResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	if _, err := client.Reset(ctx, &simpb.ResetRequest{WorldId: req.GetWorldId()}); err != nil {
		return nil, s.backendErr(err)
	}
	return &agentpbv2.ResetSimulationResponse{}, nil
}

// --- Models ---

func (s *SimService) ImportModel(stream agentpbv2.WendySimService_ImportModelServer) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	backendStream, err := client.LoadModel(stream.Context())
	if err != nil {
		return s.backendErr(err)
	}
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if sendErr := backendStream.Send(chunk); sendErr != nil {
			// Send failing means the backend closed the stream; the real
			// error comes from CloseAndRecv.
			break
		}
	}
	resp, err := backendStream.CloseAndRecv()
	if err != nil {
		return s.backendErr(err)
	}
	return stream.SendAndClose(resp)
}

func (s *SimService) DescribeModel(ctx context.Context, req *simpb.DescribeModelRequest) (*simpb.DescribeModelResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.DescribeModel(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) SpawnRobot(ctx context.Context, req *simpb.SpawnRequest) (*simpb.SpawnResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.Spawn(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}

	s.mu.Lock()
	if w, ok := s.worlds[req.GetWorldId()]; ok {
		w.robotIDs = append(w.robotIDs, resp.GetRobotId())
	}
	s.mu.Unlock()
	return resp, nil
}

// --- Observation ---

func (s *SimService) GetState(ctx context.Context, req *simpb.GetStateRequest) (*simpb.GetStateResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.GetState(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) GetContacts(ctx context.Context, req *simpb.GetContactsRequest) (*simpb.GetContactsResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.GetContacts(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) GetCameraFrame(ctx context.Context, req *simpb.GetCameraFrameRequest) (*simpb.GetCameraFrameResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.GetCameraFrame(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

// --- Control ---

func (s *SimService) SetVelocity(ctx context.Context, req *simpb.SetVelocityRequest) (*simpb.SetVelocityResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.SetVelocity(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) SetJointTargets(ctx context.Context, req *simpb.SetJointTargetsRequest) (*simpb.SetJointTargetsResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.SetJointTargets(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) Step(ctx context.Context, req *simpb.StepRequest) (*simpb.StepResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.Step(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

// --- Interactive tooling ---

func (s *SimService) SetClock(ctx context.Context, req *simpb.SetClockRequest) (*simpb.SetClockResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.SetClock(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) ApplyForce(ctx context.Context, req *simpb.ApplyForceRequest) (*simpb.ApplyForceResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.ApplyForce(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) Teleport(ctx context.Context, req *simpb.TeleportRequest) (*simpb.TeleportResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.Teleport(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) SaveSnapshot(ctx context.Context, req *simpb.SaveSnapshotRequest) (*simpb.SaveSnapshotResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.SaveSnapshot(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) RestoreSnapshot(ctx context.Context, req *simpb.RestoreSnapshotRequest) (*simpb.RestoreSnapshotResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.RestoreSnapshot(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) ReadSensors(ctx context.Context, req *simpb.ReadSensorsRequest) (*simpb.ReadSensorsResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.ReadSensors(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) EditScene(ctx context.Context, req *simpb.EditSceneRequest) (*simpb.EditSceneResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.EditScene(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) LoadPolicy(stream agentpbv2.WendySimService_LoadPolicyServer) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	backendStream, err := client.LoadPolicy(stream.Context())
	if err != nil {
		return s.backendErr(err)
	}
	for {
		chunk, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr
		}
		if sendErr := backendStream.Send(chunk); sendErr != nil {
			// Send failing means the backend closed the stream; the real
			// error comes from CloseAndRecv.
			break
		}
	}
	resp, err := backendStream.CloseAndRecv()
	if err != nil {
		return s.backendErr(err)
	}
	return stream.SendAndClose(resp)
}

func (s *SimService) ClearPolicy(ctx context.Context, req *simpb.ClearPolicyRequest) (*simpb.ClearPolicyResponse, error) {
	client, err := s.client()
	if err != nil {
		return nil, err
	}
	resp, err := client.ClearPolicy(ctx, req)
	if err != nil {
		return nil, s.backendErr(err)
	}
	return resp, nil
}

func (s *SimService) RenderVideo(req *simpb.RenderVideoRequest, stream agentpbv2.WendySimService_RenderVideoServer) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	backendStream, err := client.RenderVideo(stream.Context(), req)
	if err != nil {
		return s.backendErr(err)
	}
	for {
		chunk, recvErr := backendStream.Recv()
		if recvErr == io.EOF {
			return nil
		}
		if recvErr != nil {
			return s.backendErr(recvErr)
		}
		if sendErr := stream.Send(chunk); sendErr != nil {
			return sendErr
		}
	}
}

// --- Tasks & replay ---

func (s *SimService) RunTask(req *agentpbv2.RunSimTaskRequest, stream agentpbv2.WendySimService_RunTaskServer) error {
	task := req.GetTask()
	if task == nil {
		return status.Error(codes.InvalidArgument, "task is required")
	}
	// Clamp the task's control level to the session's entitlement: a session
	// level, when set, is a ceiling on how low-level the task may drive the
	// robot.
	if sess := req.GetSessionControlLevel(); sess != simpb.ControlLevel_CONTROL_LEVEL_UNSPECIFIED &&
		sess < task.GetMaxControlLevel() {
		task.MaxControlLevel = sess
	}

	client, err := s.client()
	if err != nil {
		return err
	}
	backendStream, err := client.RunTask(stream.Context(), task)
	if err != nil {
		return s.backendErr(err)
	}
	for {
		event, recvErr := backendStream.Recv()
		if recvErr == io.EOF {
			return nil
		}
		if recvErr != nil {
			return s.backendErr(recvErr)
		}
		if sendErr := stream.Send(event); sendErr != nil {
			return sendErr
		}
	}
}

func (s *SimService) GetReplay(req *agentpbv2.GetReplayRequest, stream agentpbv2.WendySimService_GetReplayServer) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	backendStream, err := client.GetReplay(stream.Context(), &simpb.GetReplayRequest{
		ReplayId: req.GetReplayId(),
	})
	if err != nil {
		return s.backendErr(err)
	}
	for {
		chunk, recvErr := backendStream.Recv()
		if recvErr == io.EOF {
			return nil
		}
		if recvErr != nil {
			return s.backendErr(recvErr)
		}
		if sendErr := stream.Send(&agentpbv2.GetReplayChunk{Data: chunk.GetData()}); sendErr != nil {
			return sendErr
		}
	}
}
