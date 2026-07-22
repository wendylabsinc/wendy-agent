package services

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

type recordingEventPublisher struct {
	mu       sync.Mutex
	requests []*cloudpb.PublishEventRequest
}

func (p *recordingEventPublisher) PublishEvent(
	_ context.Context,
	request *cloudpb.PublishEventRequest,
) (*cloudpb.PublishEventResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, request)
	return &cloudpb.PublishEventResponse{
		Event: &cloudpb.Event{AppId: request.GetAppId()},
	}, nil
}

func TestWendyEventServiceAttributesSourceApp(t *testing.T) {
	publisher := &recordingEventPublisher{}
	service := NewWendyEventService("dev.wendy.firewatch", publisher)

	response, err := service.PublishEvent(context.Background(), &agentpb.PublishAppEventRequest{
		SourceEventId: "fire-1",
		Title:         "FireWatch",
		Body:          "Potential fire detected",
		Severity:      cloudpb.EventSeverity_EVENT_SEVERITY_CRITICAL,
		Target: &cloudpb.EventTarget{Destination: &cloudpb.EventTarget_Live{
			Live: &cloudpb.LiveEventTarget{CameraId: "libcamera:front"},
		}},
	})
	if err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}
	if got := response.GetEvent().GetAppId(); got != "dev.wendy.firewatch" {
		t.Fatalf("response app_id = %q", got)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if got := len(publisher.requests); got != 1 {
		t.Fatalf("publisher requests = %d", got)
	}
	if got := publisher.requests[0].GetAppId(); got != "dev.wendy.firewatch" {
		t.Fatalf("forwarded app_id = %q", got)
	}
}

func TestAppEventSocketManagerUsesCollisionProofIdentityPaths(t *testing.T) {
	oldRoot := AppEventSocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-events-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		AppEventSocketRootPath = oldRoot
		os.RemoveAll(root)
	})
	AppEventSocketRootPath = root

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := NewAppEventSocketManager(ctx, zap.NewNop(), &recordingEventPublisher{})
	appDirectory, err := manager.Ensure("foo_bar", "")
	if err != nil {
		t.Fatalf("Ensure single app: %v", err)
	}
	serviceDirectory, err := manager.Ensure("foo", "bar")
	if err != nil {
		t.Fatalf("Ensure app service: %v", err)
	}
	if appDirectory == serviceDirectory {
		t.Fatal("distinct app identities resolved to the same event socket")
	}
}

func TestAppEventSocketManagerAuthenticatesBySocketMount(t *testing.T) {
	oldRoot := AppEventSocketRootPath
	root, err := os.MkdirTemp("/tmp", "wendy-events-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		AppEventSocketRootPath = oldRoot
		os.RemoveAll(root)
	})
	AppEventSocketRootPath = root

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := &recordingEventPublisher{}
	manager := NewAppEventSocketManager(ctx, zap.NewNop(), publisher)
	directory, err := manager.Ensure("dev.wendy.firewatch", "detector")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	connection, err := grpc.NewClient(
		"unix://"+filepath.Join(directory, eventSocketFilename),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer connection.Close()

	_, err = agentpb.NewWendyEventServiceClient(connection).PublishEvent(
		context.Background(),
		&agentpb.PublishAppEventRequest{SourceEventId: "detected-1"},
	)
	if err != nil {
		t.Fatalf("PublishEvent over app socket: %v", err)
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if got := publisher.requests[0].GetAppId(); got != "dev.wendy.firewatch" {
		t.Fatalf("socket attributed app_id = %q", got)
	}
}
