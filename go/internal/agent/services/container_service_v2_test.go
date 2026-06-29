package services

import (
	"context"
	"io"
	"net"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func TestMapAppContainerToV2_CarriesProvenance(t *testing.T) {
	v1 := &agentpb.AppContainer{
		AppName:       "cam",
		AppVersion:    "0.1.0",
		RunningState:  agentpb.AppRunningState_RUNNING,
		FailureCount:  2,
		DeployedAt:    "2026-06-28T20:42:00Z",
		DeployedBy:    "wendy/user/42 (org 7)",
		LastStartedAt: "2026-06-29T03:52:00Z",
		RestartCount:  2,
	}
	got := mapAppContainerToV2(v1)

	if got.RunningState != agentpbv2.AppRunningState_APP_RUNNING_STATE_RUNNING {
		t.Errorf("running state = %v; want v2 RUNNING", got.RunningState)
	}
	if got.DeployedAt != v1.DeployedAt || got.DeployedBy != v1.DeployedBy ||
		got.LastStartedAt != v1.LastStartedAt || got.RestartCount != v1.RestartCount ||
		got.FailureCount != v1.FailureCount {
		t.Errorf("provenance not carried through: %+v", got)
	}
}

func startContainerV2Server(t *testing.T, client ContainerdClient) (agentpbv2.WendyContainerServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	v1svc := NewContainerService(zap.NewNop(), client)
	svc := NewContainerServiceV2(v1svc)
	agentpbv2.RegisterWendyContainerServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return agentpbv2.NewWendyContainerServiceClient(conn), func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
}

func TestContainerServiceV2_StopContainer_NoClient(t *testing.T) {
	client, cleanup := startContainerV2Server(t, nil)
	defer cleanup()

	_, err := client.StopContainer(context.Background(), &agentpbv2.StopContainerRequest{AppName: "myapp"})
	if status.Code(err) != codes.Internal {
		t.Errorf("error code = %v; want Internal", status.Code(err))
	}
}

func TestContainerServiceV2_ListContainers_Empty(t *testing.T) {
	mc := &mockContainerdClient{}
	client, cleanup := startContainerV2Server(t, mc)
	defer cleanup()

	stream, err := client.ListContainers(context.Background(), &agentpbv2.ListContainersRequest{})
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Errorf("expected EOF for empty list, got %v", err)
	}
}
