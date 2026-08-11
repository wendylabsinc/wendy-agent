package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	systempb "github.com/wendylabsinc/wendy/go/proto/gen/systempb"
)

type systemAPITestVideoService struct {
	agentpb.UnimplementedWendyVideoServiceServer
}

func (s *systemAPITestVideoService) ListVideoDevices(
	context.Context,
	*agentpb.ListVideoDevicesRequest,
) (*agentpb.ListVideoDevicesResponse, error) {
	return &agentpb.ListVideoDevicesResponse{Devices: []*agentpb.VideoDevice{{
		Id: 200, Name: "test-ip-camera", Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP,
	}}}, nil
}

func (s *systemAPITestVideoService) StreamVideo(
	_ *agentpb.StreamVideoRequest,
	stream grpc.ServerStreamingServer[agentpb.VideoFrame],
) error {
	return stream.Send(&agentpb.VideoFrame{Data: []byte("frame"), TimestampNs: 123})
}

func TestCameraCapabilityAllowsOnlyReadOnlyVideoRPCs(t *testing.T) {
	oldRoot := AppSystemAPISocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-system-camera-allow-")
	if err != nil {
		t.Fatal(err)
	}
	AppSystemAPISocketRootPath = root
	t.Cleanup(func() {
		AppSystemAPISocketRootPath = oldRoot
		_ = os.RemoveAll(root)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewAppSystemAPISocketManager(
		ctx,
		zap.NewNop(),
		&recordingNotificationSender{},
		&systemAPITestVideoService{},
	)
	directory, err := manager.Ensure(
		"com.example.camera",
		"viewer",
		[]string{SystemAPICapabilityCamera},
	)
	if err != nil {
		t.Fatalf("Ensure(camera) error = %v", err)
	}

	conn, err := grpc.NewClient(
		"unix://"+filepath.Join(directory, SystemAPISocketFilename),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial System API: %v", err)
	}
	defer conn.Close()
	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	video := agentpb.NewWendyVideoServiceClient(conn)

	devices, err := video.ListVideoDevices(callCtx, &agentpb.ListVideoDevicesRequest{})
	if err != nil {
		t.Fatalf("ListVideoDevices() error = %v", err)
	}
	if len(devices.GetDevices()) != 1 || devices.GetDevices()[0].GetId() != 200 {
		t.Fatalf("ListVideoDevices() = %+v", devices.GetDevices())
	}
	stream, err := video.StreamVideo(callCtx, &agentpb.StreamVideoRequest{DeviceId: 200})
	if err != nil {
		t.Fatalf("StreamVideo() error = %v", err)
	}
	frame, err := stream.Recv()
	if err != nil || string(frame.GetData()) != "frame" {
		t.Fatalf("StreamVideo().Recv() = data %q, err %v", frame.GetData(), err)
	}

	_, err = video.SetCameraCredentials(callCtx, &agentpb.SetCameraCredentialsRequest{
		DeviceId: 200, Username: "admin", Password: "must-not-reach-service",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("SetCameraCredentials() code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
	_, err = systempb.NewNotificationServiceClient(conn).Send(
		callCtx,
		validSystemNotificationRequest(),
	)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Notification Send() code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}

func TestNotificationsCapabilityCannotReadVideo(t *testing.T) {
	oldRoot := AppSystemAPISocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-system-camera-deny-")
	if err != nil {
		t.Fatal(err)
	}
	AppSystemAPISocketRootPath = root
	t.Cleanup(func() {
		AppSystemAPISocketRootPath = oldRoot
		_ = os.RemoveAll(root)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewAppSystemAPISocketManager(
		ctx,
		zap.NewNop(),
		&recordingNotificationSender{},
		&systemAPITestVideoService{},
	)
	directory, err := manager.Ensure(
		"com.example.notifications",
		"",
		[]string{SystemAPICapabilityNotifications},
	)
	if err != nil {
		t.Fatalf("Ensure(notifications) error = %v", err)
	}
	conn, err := grpc.NewClient(
		"unix://"+filepath.Join(directory, SystemAPISocketFilename),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial System API: %v", err)
	}
	defer conn.Close()
	callCtx, callCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer callCancel()
	_, err = agentpb.NewWendyVideoServiceClient(conn).ListVideoDevices(
		callCtx,
		&agentpb.ListVideoDevicesRequest{},
	)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ListVideoDevices() code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
}
