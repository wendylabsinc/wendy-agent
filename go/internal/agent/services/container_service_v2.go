package services

import (
	"context"
	"io"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// ContainerServiceV2 implements agentpbv2.WendyContainerServiceServer by
// delegating to the v1 ContainerService where possible.
type ContainerServiceV2 struct {
	agentpbv2.UnimplementedWendyContainerServiceServer
	v1 *ContainerService
}

func NewContainerServiceV2(v1 *ContainerService) *ContainerServiceV2 {
	return &ContainerServiceV2{v1: v1}
}

func (s *ContainerServiceV2) StartContainer(req *agentpbv2.StartContainerRequest, stream grpc.ServerStreamingServer[agentpbv2.ContainerStreamResponse]) error {
	var restartPolicy *agentpb.RestartPolicy
	if rp := req.GetRestartPolicy(); rp != nil {
		restartPolicy = &agentpb.RestartPolicy{
			Mode:                agentpb.RestartPolicyMode(rp.GetMode()),
			OnFailureMaxRetries: rp.GetOnFailureMaxRetries(),
		}
	}
	exitCode, err := s.v1.streamContainerOutput(stream.Context(), req.GetAppName(), postStartAgentHookFromContext(stream.Context()), restartPolicy, &containerStreamV1Adapter{v2stream: stream})
	if err != nil {
		return err
	}
	return stream.Send(&agentpbv2.ContainerStreamResponse{
		ResponseType: &agentpbv2.ContainerStreamResponse_Exited_{
			Exited: &agentpbv2.ContainerStreamResponse_Exited{ExitCode: exitCode},
		},
	})
}

func (s *ContainerServiceV2) AttachContainer(stream grpc.BidiStreamingServer[agentpbv2.AttachContainerRequest, agentpbv2.ContainerStreamResponse]) error {
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
	stdinR, stdinW := io.Pipe()
	defer stdinR.Close()

	go func() {
		defer stdinW.Close()
		for {
			msg, recvErr := stream.Recv()
			if recvErr != nil {
				return
			}
			if data := msg.GetStdinData(); len(data) > 0 {
				if _, writeErr := stdinW.Write(data); writeErr != nil {
					return
				}
			}
		}
	}()

	outputCh, err := s.v1.containerd.StartContainerWithStdin(ctx, appName, stdinR, postStartAgentHookFromContext(ctx), nil)
	if err != nil {
		stdinR.Close()
		return status.Errorf(codes.Internal, "failed to start container: %v", err)
	}

	// Mirror v1 AttachContainer bookkeeping: clear any explicit-stop mark and
	// register with the monitor using the persisted restart policy.
	if s.v1.monitor != nil {
		s.v1.monitor.ClearExplicitStop(appName)
	}
	s.v1.registerContainerWithMonitor(ctx, appName, nil)

	if err := stream.Send(&agentpbv2.ContainerStreamResponse{
		ResponseType: &agentpbv2.ContainerStreamResponse_Started_{
			Started: &agentpbv2.ContainerStreamResponse_Started{},
		},
	}); err != nil {
		return err
	}

	var readCh <-chan ContainerOutput
	if s.v1.logManager != nil {
		subID, subCh := s.v1.logManager.Subscribe(appName)
		defer s.v1.logManager.Unsubscribe(appName, subID)
		readCh = subCh
		go func() {
			for output := range outputCh {
				s.v1.logManager.Publish(appName, output)
			}
		}()
	} else {
		readCh = outputCh
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case output, ok := <-readCh:
			if !ok {
				return status.Error(codes.Internal, "log subscriber closed before Done was delivered")
			}
			if output.Done {
				if output.Err != nil {
					return status.Errorf(codes.Internal, "container exited without a valid result: %v", output.Err)
				}
				return stream.Send(&agentpbv2.ContainerStreamResponse{
					ResponseType: &agentpbv2.ContainerStreamResponse_Exited_{
						Exited: &agentpbv2.ContainerStreamResponse_Exited{ExitCode: output.ExitCode},
					},
				})
			}
			if len(output.Stdout) > 0 {
				if err := stream.Send(&agentpbv2.ContainerStreamResponse{
					ResponseType: &agentpbv2.ContainerStreamResponse_StdoutOutput{
						StdoutOutput: &agentpbv2.ContainerStreamResponse_ConsoleOutput{Data: output.Stdout},
					},
				}); err != nil {
					return err
				}
			}
			if len(output.Stderr) > 0 {
				if err := stream.Send(&agentpbv2.ContainerStreamResponse{
					ResponseType: &agentpbv2.ContainerStreamResponse_StderrOutput{
						StderrOutput: &agentpbv2.ContainerStreamResponse_ConsoleOutput{Data: output.Stderr},
					},
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (s *ContainerServiceV2) StopContainer(ctx context.Context, req *agentpbv2.StopContainerRequest) (*agentpbv2.StopContainerResponse, error) {
	if s.v1.containerd == nil {
		return nil, status.Error(codes.Internal, "containerd is not available")
	}
	if _, err := s.v1.StopContainer(ctx, &agentpb.StopContainerRequest{
		AppName: req.GetAppName(),
	}); err != nil {
		return nil, err
	}
	return &agentpbv2.StopContainerResponse{}, nil
}

func (s *ContainerServiceV2) DeleteContainer(ctx context.Context, req *agentpbv2.DeleteContainerRequest) (*agentpbv2.DeleteContainerResponse, error) {
	if s.v1.containerd == nil {
		return nil, status.Error(codes.Internal, "containerd is not available")
	}
	if _, err := s.v1.DeleteContainer(ctx, &agentpb.DeleteContainerRequest{
		AppName:       req.GetAppName(),
		DeleteImage:   req.GetDeleteImage(),
		DeleteVolumes: req.GetDeleteVolumes(),
	}); err != nil {
		return nil, err
	}
	return &agentpbv2.DeleteContainerResponse{}, nil
}

func (s *ContainerServiceV2) ListContainers(_ *agentpbv2.ListContainersRequest, stream grpc.ServerStreamingServer[agentpbv2.ListContainersResponse]) error {
	if s.v1.containerd == nil {
		return nil
	}
	containers, err := s.v1.containerd.ListContainers(stream.Context())
	if err != nil {
		return status.Errorf(codes.Internal, "failed to list containers: %v", err)
	}
	for _, c := range containers {
		if err := stream.Send(&agentpbv2.ListContainersResponse{
			Container: mapAppContainerToV2(c),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *ContainerServiceV2) ListVolumes(ctx context.Context, _ *agentpbv2.ListVolumesRequest) (*agentpbv2.ListVolumesResponse, error) {
	v1resp, err := s.v1.ListVolumes(ctx, &agentpb.ListVolumesRequest{})
	if err != nil {
		return nil, err
	}
	v2vols := make([]*agentpbv2.VolumeInfo, len(v1resp.Volumes))
	for i, v := range v1resp.Volumes {
		v2vols[i] = &agentpbv2.VolumeInfo{
			Name:      v.Name,
			Path:      v.Path,
			SizeBytes: v.SizeBytes,
			CreatedAt: v.CreatedAt,
			UsedBy:    v.UsedBy,
		}
	}
	return &agentpbv2.ListVolumesResponse{Volumes: v2vols}, nil
}

func (s *ContainerServiceV2) RemoveVolume(ctx context.Context, req *agentpbv2.RemoveVolumeRequest) (*agentpbv2.RemoveVolumeResponse, error) {
	if _, err := s.v1.RemoveVolume(ctx, &agentpb.RemoveVolumeRequest{Name: req.GetName()}); err != nil {
		return nil, err
	}
	return &agentpbv2.RemoveVolumeResponse{}, nil
}

// ListContainerStats streams memory and storage stats for all managed containers.
func (s *ContainerServiceV2) ListContainerStats(_ *agentpbv2.ListContainerStatsRequest, stream grpc.ServerStreamingServer[agentpbv2.ListContainerStatsResponse]) error {
	if s.v1.containerd == nil {
		return nil
	}
	stats, err := s.v1.containerd.GetContainerStats(stream.Context())
	if err != nil {
		return status.Errorf(codes.Internal, "failed to get container stats: %v", err)
	}
	for _, st := range stats {
		if err := stream.Send(&agentpbv2.ListContainerStatsResponse{
			ContainerStats: &agentpbv2.ContainerStats{
				AppName:      st.AppName,
				MemoryBytes:  st.MemoryBytes,
				StorageBytes: st.StorageBytes,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// mapAppContainerToV2 converts a v1 AppContainer to its v2 equivalent,
// mapping the running state enum explicitly (v1 STOPPED=0/RUNNING=1 vs v2 STOPPED=1/RUNNING=2).
func mapAppContainerToV2(c *agentpb.AppContainer) *agentpbv2.AppContainer {
	var state agentpbv2.AppRunningState
	switch c.RunningState {
	case agentpb.AppRunningState_RUNNING:
		state = agentpbv2.AppRunningState_APP_RUNNING_STATE_RUNNING
	default:
		state = agentpbv2.AppRunningState_APP_RUNNING_STATE_STOPPED
	}
	return &agentpbv2.AppContainer{
		AppName:      c.AppName,
		AppVersion:   c.AppVersion,
		RunningState: state,
		FailureCount: c.FailureCount,
		McpPort:      c.McpPort,
	}
}

// containerStreamV1Adapter adapts a v2 ServerStreamingServer to the v1
// grpc.ServerStreamingServer[agentpb.RunContainerLayersResponse] interface,
// translating each v1 response message to its v2 equivalent before sending.
type containerStreamV1Adapter struct {
	v2stream grpc.ServerStreamingServer[agentpbv2.ContainerStreamResponse]
}

func (a *containerStreamV1Adapter) Send(resp *agentpb.RunContainerLayersResponse) error {
	switch t := resp.ResponseType.(type) {
	case *agentpb.RunContainerLayersResponse_Started_:
		_ = t
		return a.v2stream.Send(&agentpbv2.ContainerStreamResponse{
			ResponseType: &agentpbv2.ContainerStreamResponse_Started_{
				Started: &agentpbv2.ContainerStreamResponse_Started{},
			},
		})
	case *agentpb.RunContainerLayersResponse_StdoutOutput:
		return a.v2stream.Send(&agentpbv2.ContainerStreamResponse{
			ResponseType: &agentpbv2.ContainerStreamResponse_StdoutOutput{
				StdoutOutput: &agentpbv2.ContainerStreamResponse_ConsoleOutput{Data: t.StdoutOutput.Data},
			},
		})
	case *agentpb.RunContainerLayersResponse_StderrOutput:
		return a.v2stream.Send(&agentpbv2.ContainerStreamResponse{
			ResponseType: &agentpbv2.ContainerStreamResponse_StderrOutput{
				StderrOutput: &agentpbv2.ContainerStreamResponse_ConsoleOutput{Data: t.StderrOutput.Data},
			},
		})
	}
	return nil
}

func (a *containerStreamV1Adapter) SetHeader(md metadata.MD) error  { return a.v2stream.SetHeader(md) }
func (a *containerStreamV1Adapter) SendHeader(md metadata.MD) error { return a.v2stream.SendHeader(md) }
func (a *containerStreamV1Adapter) SetTrailer(md metadata.MD)       { a.v2stream.SetTrailer(md) }
func (a *containerStreamV1Adapter) Context() context.Context        { return a.v2stream.Context() }
func (a *containerStreamV1Adapter) SendMsg(m any) error             { return a.v2stream.SendMsg(m) }
func (a *containerStreamV1Adapter) RecvMsg(m any) error             { return a.v2stream.RecvMsg(m) }

// attachStreamV1Adapter adapts a v2 bidirectional attach stream to the v1
// grpc.BidiStreamingServer[agentpb.AttachContainerRequest, agentpb.RunContainerLayersResponse]
// interface. The embedded containerStreamV1Adapter supplies Send (v1->v2
// response translation) and the grpc.ServerStream methods; this type adds the
// Recv direction, translating v2 attach requests to their v1 equivalents.
type attachStreamV1Adapter struct {
	*containerStreamV1Adapter
	v2stream grpc.BidiStreamingServer[agentpbv2.AttachContainerRequest, agentpbv2.ContainerStreamResponse]
}

func (a *attachStreamV1Adapter) Recv() (*agentpb.AttachContainerRequest, error) {
	msg, err := a.v2stream.Recv()
	if err != nil {
		return nil, err
	}
	switch rt := msg.GetRequestType().(type) {
	case *agentpbv2.AttachContainerRequest_AppName:
		return &agentpb.AttachContainerRequest{
			RequestType: &agentpb.AttachContainerRequest_AppName{AppName: rt.AppName},
		}, nil
	case *agentpbv2.AttachContainerRequest_StdinData:
		return &agentpb.AttachContainerRequest{
			RequestType: &agentpb.AttachContainerRequest_StdinData{StdinData: rt.StdinData},
		}, nil
	default:
		return &agentpb.AttachContainerRequest{}, nil
	}
}
