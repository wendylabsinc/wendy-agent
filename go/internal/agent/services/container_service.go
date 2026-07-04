package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

type ContainerService struct {
	agentpb.UnimplementedWendyContainerServiceServer
	logger     *zap.Logger
	containerd ContainerdClient
	logManager *ContainerLogManager
	monitor    ContainerMonitorRegistrar

	// appMu serialises create/stop/delete operations per appID so that
	// ContainerIDsForApp and the subsequent monitor marks + containerd call are
	// atomic with respect to concurrent RPCs for the same app (TOCTOU prevention,
	// SOC2-CC6, NIST-AC-4).
	appMu appMutex
}

// appMutex provides per-app name mutual exclusion. Entries are permanent
// (never deleted) to avoid reference-counting deletion races under concurrent
// contention (SOC2-CC6, NIST-AC-4). Memory overhead is negligible: one
// *sync.Mutex per distinct appName seen during the process lifetime.
type appMutex struct {
	m sync.Map // map[string]*sync.Mutex
}

// lockApp acquires the per-app lock for appName and returns an unlock function.
func (a *appMutex) lockApp(appName string) func() {
	v, _ := a.m.LoadOrStore(appName, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func NewContainerService(logger *zap.Logger, client ContainerdClient, opts ...ContainerServiceOption) *ContainerService {
	s := &ContainerService{
		logger:     logger,
		containerd: client,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type ContainerServiceOption func(*ContainerService)

func WithLogManager(lm *ContainerLogManager) ContainerServiceOption {
	return func(s *ContainerService) {
		s.logManager = lm
	}
}

// Containers started with a restart policy are registered for automatic restart monitoring.
func WithMonitor(m ContainerMonitorRegistrar) ContainerServiceOption {
	return func(s *ContainerService) {
		s.monitor = m
	}
}

func (s *ContainerService) ListLayers(_ *agentpb.ListLayersRequest, stream grpc.ServerStreamingServer[agentpb.LayerHeader]) error {
	ctx := stream.Context()
	layers, err := s.containerd.ListLayers(ctx)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to list layers: %v", err)
	}

	for _, layer := range layers {
		if err := stream.Send(layer); err != nil {
			return err
		}
	}
	return nil
}

// Chunks are streamed directly to the content store without buffering the entire blob in memory.
func (s *ContainerService) WriteLayer(stream grpc.BidiStreamingServer[agentpb.WriteLayerRequest, agentpb.WriteLayerResponse]) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "empty layer upload stream")
	}
	if err != nil {
		return status.Errorf(codes.Internal, "error receiving first layer message: %v", err)
	}

	digest := first.GetDigest()
	if digest == "" {
		return status.Error(codes.InvalidArgument, "no digest provided in layer upload")
	}

	sr := &layerStreamReader{stream: stream, pending: first.GetData()}

	if err := s.containerd.WriteLayer(ctx, digest, sr, 0); err != nil {
		return status.Errorf(codes.Internal, "failed to write layer: %v", err)
	}

	// Drain any messages not consumed by WriteLayer (e.g. blob already existed).
	sr.drain()

	s.logger.Info("Layer written", zap.String("digest", digest))
	return stream.Send(&agentpb.WriteLayerResponse{})
}

type layerStreamReader struct {
	stream  grpc.BidiStreamingServer[agentpb.WriteLayerRequest, agentpb.WriteLayerResponse]
	pending []byte
	done    bool
}

func (r *layerStreamReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.done {
			return 0, io.EOF
		}
		msg, err := r.stream.Recv()
		if err == io.EOF {
			r.done = true
			return 0, io.EOF
		}
		if err != nil {
			return 0, err
		}
		r.pending = msg.GetData()
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// drain consumes any remaining messages from the stream without processing them.
// This is needed when WriteLayer returns early (e.g. blob already exists) so
// the gRPC stream is not left in a half-read state.
func (r *layerStreamReader) drain() {
	if r.done {
		return
	}
	for {
		if _, err := r.stream.Recv(); err != nil {
			r.done = true
			return
		}
	}
}

func (s *ContainerService) CreateContainer(ctx context.Context, req *agentpb.CreateContainerRequest) (*agentpb.CreateContainerResponse, error) {
	appCfg, err := parseAppConfig(req.GetAppConfig())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid app config: %v", err)
	}

	if err := s.containerd.CreateContainer(ctx, req, appCfg); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create container: %v", err)
	}

	s.logger.Info("Container created",
		zap.String("app_name", req.GetAppName()),
		zap.String("image", req.GetImageName()),
	)
	return &agentpb.CreateContainerResponse{}, nil
}

func (s *ContainerService) CreateContainerWithProgress(req *agentpb.CreateContainerRequest, stream grpc.ServerStreamingServer[agentpb.CreateContainerProgressResponse]) error {
	appCfg, err := parseAppConfig(req.GetAppConfig())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid app config: %v", err)
	}

	onProgress := func(p *agentpb.CreateContainerProgress) {
		if err := stream.Send(&agentpb.CreateContainerProgressResponse{
			ResponseType: &agentpb.CreateContainerProgressResponse_Progress{
				Progress: p,
			},
		}); err != nil {
			s.logger.Warn("failed to send progress update", zap.Error(err))
		}
	}

	if err := s.containerd.CreateContainerWithProgress(stream.Context(), req, appCfg, onProgress); err != nil {
		return status.Errorf(codes.Internal, "failed to create container: %v", err)
	}

	// Send completed response.
	return stream.Send(&agentpb.CreateContainerProgressResponse{
		ResponseType: &agentpb.CreateContainerProgressResponse_Completed{
			Completed: &agentpb.CreateContainerResponse{},
		},
	})
}

// to32 converts a wire hash (raw 32 bytes) to a fixed array, rejecting bad sizes.
func to32(b []byte) ([32]byte, error) {
	var a [32]byte
	if len(b) != 32 {
		return a, status.Errorf(codes.InvalidArgument, "chunk hash must be 32 bytes, got %d", len(b))
	}
	copy(a[:], b)
	return a, nil
}

func (s *ContainerService) QueryChunks(ctx context.Context, req *agentpb.QueryChunksRequest) (*agentpb.QueryChunksResponse, error) {
	hashes := make([][32]byte, 0, len(req.GetChunkHashes()))
	for _, b := range req.GetChunkHashes() {
		h, err := to32(b)
		if err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	missing, err := s.containerd.MissingChunks(ctx, hashes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "querying chunks: %v", err)
	}
	out := make([][]byte, 0, len(missing))
	for _, h := range missing {
		hb := h
		out = append(out, hb[:])
	}
	return &agentpb.QueryChunksResponse{MissingHashes: out}, nil
}

func (s *ContainerService) QueryLayers(ctx context.Context, req *agentpb.QueryLayersRequest) (*agentpb.QueryLayersResponse, error) {
	present, err := s.containerd.PresentLayers(ctx, req.GetDiffIds())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "querying layers: %v", err)
	}
	out := make([]*agentpb.PresentLayer, 0, len(present))
	for diffID, size := range present {
		out = append(out, &agentpb.PresentLayer{DiffId: diffID, Size: size})
	}
	return &agentpb.QueryLayersResponse{Present: out}, nil
}

func (s *ContainerService) WriteChunks(stream grpc.ClientStreamingServer[agentpb.WriteChunksRequest, agentpb.WriteChunksResponse]) error {
	ctx := stream.Context()
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&agentpb.WriteChunksResponse{})
		}
		if err != nil {
			return err
		}
		h, err := to32(msg.GetHash())
		if err != nil {
			return err
		}
		if err := s.containerd.StageChunk(ctx, h, msg.GetData()); err != nil {
			// Preserve an explicit gRPC code from the store (e.g. ResourceExhausted
			// when a staging limit is hit); otherwise treat it as a bad chunk.
			if _, ok := status.FromError(err); ok {
				return err
			}
			return status.Errorf(codes.InvalidArgument, "staging chunk: %v", err)
		}
	}
}

func (s *ContainerService) RunContainer(req *agentpb.RunContainerLayersRequest, stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse]) error {
	ctx := stream.Context()

	appCfg, err := parseAppConfig(req.GetAppConfig())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid app config: %v", err)
	}

	if layers := req.GetLayers(); len(layers) > 0 {
		for _, l := range layers {
			if hs := l.GetChunkHashes(); len(hs) > 0 {
				order := make([][32]byte, 0, len(hs))
				for _, b := range hs {
					h, err := to32(b)
					if err != nil {
						return err
					}
					order = append(order, h)
				}
				diffID := l.GetDiffId()
				if diffID == "" {
					diffID = l.GetDigest()
				}
				if err := s.containerd.AssembleLayerFromChunks(ctx, diffID, order); err != nil {
					return status.Errorf(codes.Internal, "reassembling layer %s: %v", diffID, err)
				}
			}
		}
		if err := s.containerd.AssembleImage(ctx, req.GetImageName(), layers, req.GetImageConfig()); err != nil {
			return status.Errorf(codes.Internal, "failed to assemble image: %v", err)
		}
	}

	// Note: RunContainerLayersRequest has no Env field; env vars from callers
	// using this legacy path (wendy run with layer upload) are not forwarded.
	// Compose deployments use CreateContainerWithProgress which does carry Env.
	createReq := &agentpb.CreateContainerRequest{
		ImageName:     req.GetImageName(),
		AppName:       req.GetAppName(),
		Cmd:           req.GetCmd(),
		AppConfig:     req.GetAppConfig(),
		WorkingDir:    req.GetWorkingDir(),
		RestartPolicy: req.GetRestartPolicy(),
		UserArgs:      req.GetUserArgs(),
	}

	if err := s.containerd.CreateContainer(ctx, createReq, appCfg); err != nil {
		return status.Errorf(codes.Internal, "failed to create container: %v", err)
	}

	return s.streamContainerOutput(ctx, req.GetAppName(), postStartAgentHookFromContext(ctx), nil, stream)
}

func (s *ContainerService) StartContainer(req *agentpb.StartContainerRequest, stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse]) error {
	appName := req.GetAppName()
	ctx := stream.Context()

	// Multi-service groups: start each service container without streaming.
	// Streaming output from multiple containers concurrently is not supported;
	// group start behaves like --detach for every service.
	ids, err := s.containerd.ContainerIDsForApp(ctx, appName)
	if err == nil && len(ids) > 1 {
		return s.startGroup(ctx, appName, ids, req.GetRestartPolicy(), stream)
	}

	return s.streamContainerOutput(ctx, appName, postStartAgentHookFromContext(ctx), req.GetRestartPolicy(), stream)
}

// startGroup starts each service container in a multi-service app in detach
// mode and sends a single Started response. Output from individual services
// is discarded; use wendy device logs to tail per-service logs.
func (s *ContainerService) startGroup(
	ctx context.Context,
	appName string,
	containerIDs []string,
	restartPolicy *agentpb.RestartPolicy,
	stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse],
) error {
	unlock := s.appMu.lockApp(appName)
	defer unlock()

	for _, id := range containerIDs {
		outputCh, startErr := s.containerd.StartContainer(ctx, id, "", restartPolicy)
		if startErr != nil {
			return status.Errorf(codes.Internal, "failed to start service %q: %v", id, startErr)
		}
		// Drain the output channel in the background so the containerd goroutine
		// does not block. The container runs independently after this.
		go func(ch <-chan ContainerOutput) {
			for range ch {
			}
		}(outputCh)

		if s.monitor != nil {
			s.monitor.ClearExplicitStop(id)
		}
		if err := s.containerd.SetStoppedByUser(ctx, id, false); err != nil {
			s.logger.Warn("failed to clear stopped-by-user mark",
				zap.String("container_id", id), zap.Error(err))
		}
		s.registerContainerWithMonitor(ctx, id, restartPolicy)
	}

	return stream.Send(&agentpb.RunContainerLayersResponse{
		ResponseType: &agentpb.RunContainerLayersResponse_Started_{
			Started: &agentpb.RunContainerLayersResponse_Started{},
		},
	})
}

func postStartAgentHookFromContext(ctx context.Context) string {
	values := metadata.ValueFromIncomingContext(ctx, appconfig.PostStartAgentHookMetadataKey)
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1]
}

// monitorPolicyIntFromLabel converts a raw restart policy label string (e.g.
// "unless-stopped", "on-failure:5", "no") to the integer constants used by
// ContainerMonitorRegistrar. Returns ok=false for an empty or "no" label.
func monitorPolicyIntFromLabel(label string) (policy int, maxRetries int, ok bool) {
	if label == "" || label == "no" {
		return RestartPolicyNo, 0, false
	}
	policyStr, retries, parseOK := parseRestartPolicyLabel(label)
	if !parseOK {
		return 0, 0, false
	}
	switch policyStr {
	case "always":
		return RestartPolicyAlways, 0, true
	case "unless-stopped":
		return RestartPolicyUnlessStopped, 0, true
	case "on-failure":
		return RestartPolicyOnFailure, retries, true
	default:
		return 0, 0, false
	}
}

// parseRestartPolicyLabel splits a label like "on-failure:5" into ("on-failure", 5, true).
// Returns ok=false when the retry count portion cannot be parsed as a non-negative integer.
func parseRestartPolicyLabel(label string) (policyStr string, retries int, ok bool) {
	parts := strings.SplitN(label, ":", 2)
	if len(parts) == 1 {
		return label, 0, true
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return "", 0, false
	}
	return parts[0], n, true
}

// Returns ok=false when the policy should not be registered (nil or explicit NO).
func monitorPolicyInt(rp *agentpb.RestartPolicy) (policy int, maxRetries int, ok bool) {
	if rp == nil {
		return RestartPolicyNo, 0, false
	}
	switch rp.GetMode() {
	case agentpb.RestartPolicyMode_NO:
		return RestartPolicyNo, 0, false
	case agentpb.RestartPolicyMode_DEFAULT, agentpb.RestartPolicyMode_UNLESS_STOPPED:
		return RestartPolicyUnlessStopped, 0, true
	case agentpb.RestartPolicyMode_ON_FAILURE:
		retries := int(rp.GetOnFailureMaxRetries())
		if retries < 0 {
			retries = 0
		}
		return RestartPolicyOnFailure, retries, true
	default:
		return 0, 0, false // unknown mode — treat as no policy
	}
}

// When restartPolicy is nil the persisted containerd label is used.
func (s *ContainerService) registerContainerWithMonitor(ctx context.Context, appName string, restartPolicy *agentpb.RestartPolicy) {
	if s.monitor == nil {
		return
	}
	if policy, maxRetries, ok := monitorPolicyInt(restartPolicy); ok {
		s.monitor.Register(appName, policy, maxRetries)
	} else if restartPolicy != nil && restartPolicy.GetMode() == agentpb.RestartPolicyMode_NO {
		// Explicit NO — remove any existing registration.
		s.monitor.Unregister(appName)
	} else {
		// nil restartPolicy or unrecognized/forward-compatible mode: fall back
		// to the persisted label so we don't accidentally clear an existing
		// registration when a newer client sends a mode this agent doesn't know.
		if label, labelErr := s.containerd.GetContainerRestartPolicyLabel(ctx, appName); labelErr == nil {
			if label == "" || label == "no" {
				// No restart policy persisted — clear any stale registration.
				s.monitor.Unregister(appName)
			} else if policy, maxRetries, ok := monitorPolicyIntFromLabel(label); ok {
				s.monitor.Register(appName, policy, maxRetries)
			} else {
				// Unknown label value — treat as no restart policy.
				s.monitor.Unregister(appName)
			}
		} else {
			s.logger.Warn("failed to read restart policy label; monitor registration skipped",
				zap.String("app_name", appName),
				zap.Error(labelErr),
			)
		}
	}
}

// When a ContainerLogManager is configured, reads from the log manager subscription
// instead of directly from containerd, enabling multi-subscriber fan-out and telemetry bridging.
func (s *ContainerService) streamContainerOutput(
	ctx context.Context,
	appName string,
	postStartAgentCommand string,
	restartPolicy *agentpb.RestartPolicy,
	stream grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse],
) error {
	outputCh, err := s.containerd.StartContainer(ctx, appName, postStartAgentCommand, restartPolicy)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to start container: %v", err)
	}

	// The container started successfully. If it was previously explicitly
	// stopped, clear that mark so automatic restarts are re-enabled.
	// We clear it here (right after start succeeds) rather than after
	// streaming completes so that stream errors (e.g. client disconnect)
	// do not leave ExplicitStop set and suppress future automatic restarts.
	if s.monitor != nil {
		s.monitor.ClearExplicitStop(appName)
	}
	// Clear the persisted stop mark too, so a user-initiated start re-enables
	// boot reconcile for this app. Best-effort. (Only user starts reach this
	// path; the boot reconcile starts via the monitor and never clears it.)
	if err := s.containerd.SetStoppedByUser(ctx, appName, false); err != nil {
		s.logger.Warn("failed to clear stopped-by-user mark",
			zap.String("app_name", appName), zap.Error(err))
	}

	s.registerContainerWithMonitor(ctx, appName, restartPolicy)

	if err := stream.Send(&agentpb.RunContainerLayersResponse{
		ResponseType: &agentpb.RunContainerLayersResponse_Started_{
			Started: &agentpb.RunContainerLayersResponse_Started{},
		},
	}); err != nil {
		return err
	}

	var readCh <-chan ContainerOutput
	if s.logManager != nil {
		subID, subCh := s.logManager.Subscribe(appName)
		defer s.logManager.Unsubscribe(appName, subID)
		readCh = subCh

		go func() {
			for output := range outputCh {
				s.logManager.Publish(appName, output)
			}
			// When containerd channel closes, publish a Done marker.
			s.logManager.Publish(appName, ContainerOutput{Done: true})
		}()
	} else {
		readCh = outputCh
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case output, ok := <-readCh:
			if !ok || output.Done {
				return nil
			}
			if len(output.Stdout) > 0 {
				if err := stream.Send(&agentpb.RunContainerLayersResponse{
					ResponseType: &agentpb.RunContainerLayersResponse_StdoutOutput{
						StdoutOutput: &agentpb.RunContainerLayersResponse_ConsoleOutput{
							Data: output.Stdout,
						},
					},
				}); err != nil {
					return err
				}
			}
			if len(output.Stderr) > 0 {
				if err := stream.Send(&agentpb.RunContainerLayersResponse{
					ResponseType: &agentpb.RunContainerLayersResponse_StderrOutput{
						StderrOutput: &agentpb.RunContainerLayersResponse_ConsoleOutput{
							Data: output.Stderr,
						},
					},
				}); err != nil {
					return err
				}
			}
		}
	}
}

// The first client message must set app_name; subsequent messages carry stdin data.
func (s *ContainerService) AttachContainer(stream grpc.BidiStreamingServer[agentpb.AttachContainerRequest, agentpb.RunContainerLayersResponse]) error {
	first, err := stream.Recv()
	if err == io.EOF {
		return status.Error(codes.InvalidArgument, "missing first attach message")
	}
	if err != nil {
		return err
	}
	appName := first.GetAppName()
	if appName == "" {
		return status.Error(codes.InvalidArgument, "app_name required as first message")
	}

	ctx := stream.Context()
	postStartAgentCommand := postStartAgentHookFromContext(ctx)

	stdinR, stdinW := io.Pipe()
	defer stdinR.Close()

	go func() {
		defer stdinW.Close()
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				return // client disconnected or closed send
			}
			if data := msg.GetStdinData(); len(data) > 0 {
				if _, writeErr := stdinW.Write(data); writeErr != nil {
					return
				}
			}
		}
	}()

	outputCh, err := s.containerd.StartContainerWithStdin(ctx, appName, stdinR, postStartAgentCommand, nil)
	if err != nil {
		stdinR.Close()
		return status.Errorf(codes.Internal, "failed to start container: %v", err)
	}

	// Mirror the same monitor bookkeeping as streamContainerOutput: clear any
	// prior explicit-stop mark and register with the persisted restart policy.
	if s.monitor != nil {
		s.monitor.ClearExplicitStop(appName)
	}
	if err := s.containerd.SetStoppedByUser(ctx, appName, false); err != nil {
		s.logger.Warn("failed to clear stopped-by-user mark",
			zap.String("app_name", appName), zap.Error(err))
	}
	s.registerContainerWithMonitor(ctx, appName, nil)

	if err := stream.Send(&agentpb.RunContainerLayersResponse{
		ResponseType: &agentpb.RunContainerLayersResponse_Started_{
			Started: &agentpb.RunContainerLayersResponse_Started{},
		},
	}); err != nil {
		return err
	}

	var readCh <-chan ContainerOutput
	if s.logManager != nil {
		subID, subCh := s.logManager.Subscribe(appName)
		defer s.logManager.Unsubscribe(appName, subID)
		readCh = subCh

		go func() {
			for output := range outputCh {
				s.logManager.Publish(appName, output)
			}
			s.logManager.Publish(appName, ContainerOutput{Done: true})
		}()
	} else {
		readCh = outputCh
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case output, ok := <-readCh:
			if !ok || output.Done {
				return nil
			}
			if len(output.Stdout) > 0 {
				if err := stream.Send(&agentpb.RunContainerLayersResponse{
					ResponseType: &agentpb.RunContainerLayersResponse_StdoutOutput{
						StdoutOutput: &agentpb.RunContainerLayersResponse_ConsoleOutput{
							Data: output.Stdout,
						},
					},
				}); err != nil {
					return err
				}
			}
			if len(output.Stderr) > 0 {
				if err := stream.Send(&agentpb.RunContainerLayersResponse{
					ResponseType: &agentpb.RunContainerLayersResponse_StderrOutput{
						StderrOutput: &agentpb.RunContainerLayersResponse_ConsoleOutput{
							Data: output.Stderr,
						},
					},
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (s *ContainerService) StopContainer(ctx context.Context, req *agentpb.StopContainerRequest) (*agentpb.StopContainerResponse, error) {
	appName := req.GetAppName()
	// Validate before acquiring the mutex so that invalid names never reach the
	// appMutex map (preventing unbounded map growth from adversarial RPC input,
	// SOC2-CC6, NIST-SC-5) and so the monitor fallback below always receives a
	// well-formed identifier (SOC2-CC6 INFORMATIONAL-12).
	if err := appconfig.ValidateAppID(appName); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid app name: %v", err)
	}

	// Hold the per-app lock so that ContainerIDsForApp, MarkExplicitStop, and
	// the actual stop are atomic with respect to concurrent CreateContainer or
	// DeleteContainer calls for the same app (SOC2-CC6, NIST-AC-4).
	unlock := s.appMu.lockApp(appName)
	defer unlock()

	// Resolve every container ID that belongs to this app (one for
	// single-container apps, one per service for multi-service apps) so the
	// monitor can mark each before any stop is issued. Marking only the bare
	// appName would miss {appID}_{serviceName} entries registered by the monitor.
	ids, err := s.containerd.ContainerIDsForApp(ctx, appName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolving containers for app %q: %v", appName, err)
	}
	if len(ids) == 0 {
		ids = []string{appName}
	}

	// Mark BEFORE stop so the monitor cannot observe the exit and restart in
	// the window between StopContainer returning and MarkExplicitStop being
	// called. Revert all marks if the stop ultimately fails.
	if s.monitor != nil {
		for _, id := range ids {
			s.monitor.MarkExplicitStop(id)
		}
	}
	if err := s.containerd.StopContainer(ctx, appName); err != nil {
		if s.monitor != nil {
			for _, id := range ids {
				s.monitor.ClearExplicitStop(id)
			}
		}
		return nil, status.Errorf(codes.Internal, "failed to stop container: %v", err)
	}
	// Persist the stop so it survives a reboot: the boot reconcile skips
	// containers carrying this mark, so a deliberate stop is not undone by the
	// restart policy. Best-effort — a label failure must not fail the stop.
	for _, id := range ids {
		if err := s.containerd.SetStoppedByUser(ctx, id, true); err != nil {
			s.logger.Warn("failed to persist stopped-by-user mark",
				zap.String("container_id", id), zap.Error(err))
		}
	}
	s.logger.Info("App stopped", zap.String("app_name", appName), zap.Int("service_count", len(ids)))
	return &agentpb.StopContainerResponse{}, nil
}

func (s *ContainerService) DeleteContainer(ctx context.Context, req *agentpb.DeleteContainerRequest) (*agentpb.DeleteContainerResponse, error) {
	appName := req.GetAppName()
	if err := appconfig.ValidateAppID(appName); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid app name: %v", err)
	}

	// Hold the per-app lock so that ContainerIDsForApp, monitor unregister, and
	// the actual delete are atomic with respect to concurrent CreateContainer or
	// StopContainer calls for the same app (SOC2-CC6, NIST-AC-4).
	unlock := s.appMu.lockApp(appName)
	defer unlock()

	// Resolve all container IDs before deletion so the monitor can unregister
	// each one. Unregistering only the bare appName would leave
	// {appID}_{serviceName} monitor entries alive and potentially trigger
	// spurious restart attempts while the container is being removed.
	ids, err := s.containerd.ContainerIDsForApp(ctx, appName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolving containers for app %q: %v", appName, err)
	}
	if len(ids) == 0 {
		ids = []string{appName}
	}

	// Unregister from the monitor BEFORE deletion to close the window where the
	// monitor could attempt a restart while containers are being removed.
	if s.monitor != nil {
		for _, id := range ids {
			s.monitor.MarkExplicitStop(id)
			s.monitor.Unregister(id)
		}
	}

	// Resolve which volumes the app owns BEFORE deleting its containers: the
	// persist entitlement labels that record ownership die with the container.
	var volumesToDelete []string
	if req.GetDeleteVolumes() {
		volumesToDelete = s.resolveDeletableVolumes(ctx, appName)
	}

	if err := s.containerd.DeleteContainer(ctx, appName, req.GetDeleteImage()); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete container: %v", err)
	}

	if req.GetDeleteVolumes() {
		s.deleteVolumes(volumesToDelete)
	}

	s.logger.Info("App deleted",
		zap.String("app_name", appName),
		zap.Bool("delete_image", req.GetDeleteImage()),
		zap.Bool("delete_volumes", req.GetDeleteVolumes()),
	)
	return &agentpb.DeleteContainerResponse{}, nil
}

// volumesDir is the base directory for persistent volumes. It's a variable
// (not const) so tests can override it with a temp directory.
var volumesDir = "/var/lib/wendy/volumes"

// resolveDeletableVolumes returns the persistent volumes to remove when
// appName is deleted with --delete-volumes: exactly the volume names the app's
// containers declare via persist entitlements, minus any name that another
// deployed app also declares (volumes are shared across apps by name — see
// oci.applyPersist). Ambiguity fails safe: when ownership cannot be resolved,
// or for apps deployed before entitlement labels existed, nothing is deleted —
// a leaked volume can still be removed with `wendy device volumes rm`, whereas
// a wrongly deleted one is unrecoverable.
func (s *ContainerService) resolveDeletableVolumes(ctx context.Context, appName string) []string {
	declared, err := s.containerd.AppDeclaredVolumes(ctx)
	if err != nil {
		s.logger.Warn("Skipping volume deletion: cannot resolve volume ownership",
			zap.String("app_name", appName), zap.Error(err))
		return nil
	}
	owned := declared[appName]
	if len(owned) == 0 {
		s.logger.Info("App declares no persistent volumes; none deleted",
			zap.String("app_name", appName))
		return nil
	}

	otherOwners := make(map[string][]string) // volume name → other apps declaring it
	for app, vols := range declared {
		if app == appName {
			continue
		}
		for _, v := range vols {
			otherOwners[v] = append(otherOwners[v], app)
		}
	}

	var deletable []string
	for _, v := range owned {
		if others := otherOwners[v]; len(others) > 0 {
			s.logger.Warn("Keeping shared volume: also declared by other apps",
				zap.String("volume", v),
				zap.String("app_name", appName),
				zap.Strings("also_declared_by", others))
			continue
		}
		deletable = append(deletable, v)
	}
	return deletable
}

// deleteVolumes removes the given volume directories under volumesDir. The
// names come from persist entitlement labels; re-apply the same base-name
// sanitization applyPersist used when creating them so no label value can
// escape the volumes directory (SOC2-CC6, ISO27001-A.8, NIST-SI-10).
func (s *ContainerService) deleteVolumes(names []string) {
	for _, name := range names {
		if name != filepath.Base(name) || name == "." || name == ".." || name == "/" || name == "" {
			s.logger.Warn("Skipping volume with unsafe name", zap.String("volume", name))
			continue
		}
		path := filepath.Join(volumesDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue // declared but never materialized on disk
		}
		if err := os.RemoveAll(path); err != nil {
			s.logger.Warn("Failed to remove volume", zap.String("path", path), zap.Error(err))
		} else {
			s.logger.Info("Volume removed", zap.String("path", path))
		}
	}
}

func (s *ContainerService) ListVolumes(ctx context.Context, _ *agentpb.ListVolumesRequest) (*agentpb.ListVolumesResponse, error) {
	entries, err := os.ReadDir(volumesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &agentpb.ListVolumesResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "reading volumes dir: %v", err)
	}

	usedBy := s.buildVolumeUsageMap(ctx)

	var volumes []*agentpb.VolumeInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		path := filepath.Join(volumesDir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}

		volumes = append(volumes, &agentpb.VolumeInfo{
			Name:      name,
			Path:      path,
			SizeBytes: dirSize(path),
			CreatedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			UsedBy:    usedBy[name],
		})
	}

	return &agentpb.ListVolumesResponse{Volumes: volumes}, nil
}

func (s *ContainerService) RemoveVolume(_ context.Context, req *agentpb.RemoveVolumeRequest) (*agentpb.RemoveVolumeResponse, error) {
	name := filepath.Base(req.GetName())
	if name == "" || name == "." || name == ".." || name == "/" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume name")
	}

	path := filepath.Join(volumesDir, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "volume %q not found", name)
		}
		return nil, status.Errorf(codes.Internal, "checking volume %q: %v", name, err)
	}

	if err := os.RemoveAll(path); err != nil {
		return nil, status.Errorf(codes.Internal, "removing volume: %v", err)
	}

	s.logger.Info("Volume removed", zap.String("name", name), zap.String("path", path))
	return &agentpb.RemoveVolumeResponse{}, nil
}

// buildVolumeUsageMap maps each volume name to the apps that declare it via
// persist entitlement labels — real ownership, not a name-prefix guess.
// Volumes are shared across apps by name, so several apps may appear for one
// volume. Volumes no deployed app declares (including those of apps deployed
// before entitlement labels existed) get an empty usedBy.
func (s *ContainerService) buildVolumeUsageMap(ctx context.Context) map[string][]string {
	usage := make(map[string][]string)
	declared, err := s.containerd.AppDeclaredVolumes(ctx)
	if err != nil {
		s.logger.Warn("Failed to resolve volume ownership", zap.Error(err))
		return usage
	}
	for app, vols := range declared {
		for _, v := range vols {
			usage[v] = append(usage[v], app)
		}
	}
	for _, apps := range usage {
		sort.Strings(apps)
	}
	return usage
}

func dirSize(path string) int64 {
	var size int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

func (s *ContainerService) ListContainerStats(ctx context.Context, _ *agentpb.ListContainerStatsRequest) (*agentpb.ListContainerStatsResponse, error) {
	stats, err := s.containerd.GetContainerStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting container stats: %v", err)
	}
	return &agentpb.ListContainerStatsResponse{Stats: stats}, nil
}

// GetResourceStats returns host CPU/memory/GPU counters plus per-container CPU
// and memory for `wendy device top`. Host metrics are best-effort: a failed
// /proc read or absent GPU tool yields zero/empty fields rather than an error,
// so the command degrades gracefully on constrained hosts.
//
// Like every other method on this service, access is gated by the agent's gRPC
// transport (the device's trusted control channel); there is no per-RPC
// authorization layer, so the read-only host topology this returns is no more
// exposed than the existing container-stats RPCs. The call is logged for audit.
func (s *ContainerService) GetResourceStats(ctx context.Context, _ *agentpb.GetResourceStatsRequest) (*agentpb.GetResourceStatsResponse, error) {
	s.logger.Info("GetResourceStats")
	containers, err := s.containerd.GetResourceStats(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting resource stats: %v", err)
	}

	host := &agentpb.HostStats{}
	if cpu, cpuErr := hoststats.ReadCPU(); cpuErr == nil {
		host.CpuTotalJiffies = cpu.TotalJiffies
		host.CpuIdleJiffies = cpu.IdleJiffies
		host.CpuCount = cpu.CPUCount
	}
	if mem, memErr := hoststats.ReadMemory(); memErr == nil {
		host.MemTotalBytes = mem.TotalBytes
		host.MemAvailableBytes = mem.AvailableBytes
	}
	host.Gpus = gpuStatsToProto(hoststats.SampleGPU(ctx))
	host.ThermalZones = thermalZonesToProto(hoststats.SampleThermal())

	return &agentpb.GetResourceStatsResponse{
		Host:       host,
		Containers: containers,
	}, nil
}

// thermalZonesToProto converts sampled thermal zones to their proto form.
func thermalZonesToProto(zones []hoststats.ThermalZone) []*agentpb.ThermalZone {
	if len(zones) == 0 {
		return nil
	}
	out := make([]*agentpb.ThermalZone, len(zones))
	for i, z := range zones {
		out[i] = &agentpb.ThermalZone{Name: z.Name, TempC: z.TempC}
	}
	return out
}

// GetContainerPorts returns the listening TCP and bound UDP sockets for the
// given app, read from each of its containers' network namespaces. Loopback-bound
// ports are intentionally included so operators can see services that are exposed
// only on localhost. Access is gated by the agent's gRPC transport, the same
// trusted control channel that secures every other method here; the call is
// logged for audit.
func (s *ContainerService) GetContainerPorts(ctx context.Context, req *agentpb.GetContainerPortsRequest) (*agentpb.GetContainerPortsResponse, error) {
	if req.GetAppName() == "" {
		return nil, status.Errorf(codes.InvalidArgument, "app_name is required")
	}
	s.logger.Info("GetContainerPorts", zap.String("app_name", req.GetAppName()))
	ports, err := s.containerd.GetListeningPorts(ctx, req.GetAppName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "getting container ports: %v", err)
	}
	return &agentpb.GetContainerPortsResponse{Ports: ports}, nil
}

func gpuStatsToProto(in []hoststats.GPUStat) []*agentpb.GpuStats {
	out := make([]*agentpb.GpuStats, 0, len(in))
	for _, g := range in {
		pg := &agentpb.GpuStats{
			Index:         g.Index,
			Name:          g.Name,
			UtilPercent:   g.UtilPercent,
			MemUsedBytes:  g.MemUsedBytes,
			MemTotalBytes: g.MemTotalBytes,
			TempC:         g.TempC,
			PowerW:        g.PowerW,
		}
		out = append(out, pg)
	}
	return out
}

func (s *ContainerService) ListContainers(_ *agentpb.ListContainersRequest, stream grpc.ServerStreamingServer[agentpb.ListContainersResponse]) error {
	containers, err := s.containerd.ListContainers(stream.Context())
	if err != nil {
		return status.Errorf(codes.Internal, "failed to list containers: %v", err)
	}

	for _, c := range containers {
		if err := stream.Send(&agentpb.ListContainersResponse{Container: c}); err != nil {
			return err
		}
	}
	return nil
}

// StreamMCP proxies a bidirectional gRPC stream to the container's MCP TCP port.
// The caller must supply an "app-name" metadata key identifying the target container.
func (s *ContainerService) StreamMCP(stream grpc.BidiStreamingServer[agentpb.MCPChunk, agentpb.MCPChunk]) error {
	ctx := stream.Context()
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("app-name")
	if len(vals) == 0 || vals[0] == "" {
		return status.Errorf(codes.InvalidArgument, "app-name metadata is required")
	}
	appName := vals[0]

	mcpPort, err := s.containerd.GetContainerMCPPort(ctx, appName)
	if err != nil {
		return status.Errorf(codes.NotFound, "container %q: %v", appName, err)
	}
	if mcpPort == 0 {
		return status.Errorf(codes.NotFound, "container %q has no mcp entitlement", appName)
	}

	// Verify the container is running before attempting to dial its MCP port.
	containers, listErr := s.containerd.ListContainers(ctx)
	if listErr != nil {
		s.logger.Warn("failed to list containers for running check in StreamMCP", zap.Error(listErr))
	} else {
		running := false
		for _, c := range containers {
			if c.GetAppName() == appName && c.GetRunningState() == agentpb.AppRunningState_RUNNING {
				running = true
				break
			}
		}
		if !running {
			return status.Errorf(codes.FailedPrecondition, "container %q is not running", appName)
		}
	}

	tcpConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", mcpPort))
	if err != nil {
		return status.Errorf(codes.Unavailable, "connecting to MCP server for %q on port %d: %v", appName, mcpPort, err)
	}
	defer tcpConn.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errc := make(chan error, 2)

	// gRPC → TCP
	go func() {
		for {
			chunk, err := stream.Recv()
			if err != nil {
				errc <- err
				return
			}
			if _, err := tcpConn.Write(chunk.Data); err != nil {
				errc <- err
				return
			}
		}
	}()

	// TCP → gRPC
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := tcpConn.Read(buf)
			if n > 0 {
				if sendErr := stream.Send(&agentpb.MCPChunk{Data: buf[:n]}); sendErr != nil {
					errc <- sendErr
					return
				}
			}
			if readErr != nil {
				errc <- readErr
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		if err == io.EOF {
			return nil
		}
		return err
	}
}

func parseAppConfig(data []byte) (*appconfig.AppConfig, error) {
	if len(data) == 0 {
		return &appconfig.AppConfig{}, nil
	}
	var cfg appconfig.AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Reject unsafe app IDs at the RPC boundary so direct callers and generated
	// compose configs can't push comma/'='/newline characters into the env vars
	// and labels derived from appId. Empty appId is left to existing behaviour.
	if cfg.AppID != "" {
		if err := appconfig.ValidateAppID(cfg.AppID); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}
