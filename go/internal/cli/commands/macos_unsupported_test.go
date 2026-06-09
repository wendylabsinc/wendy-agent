package commands

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type fakeUnsupportedAgentServer struct {
	agentpb.UnimplementedWendyAgentServiceServer
	os string
}

func (s *fakeUnsupportedAgentServer) GetAgentVersion(_ context.Context, _ *agentpb.GetAgentVersionRequest) (*agentpb.GetAgentVersionResponse, error) {
	return &agentpb.GetAgentVersionResponse{Os: s.os}, nil
}

func startUnsupportedAgentClient(t *testing.T, osName string) agentpb.WendyAgentServiceClient {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	srv := grpc.NewServer()
	agentpb.RegisterWendyAgentServiceServer(srv, &fakeUnsupportedAgentServer{os: osName})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = ln.Close()
	})

	conn, err := grpc.NewClient(ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return agentpb.NewWendyAgentServiceClient(conn)
}

func TestMacOSBetaUnsupportedFeatureError_ReturnsMacBetaMessage(t *testing.T) {
	ctx := context.Background()
	client := startUnsupportedAgentClient(t, "darwin")

	_, rpcErr := client.ListWiFiNetworks(ctx, &agentpb.ListWiFiNetworksRequest{})
	if status.Code(rpcErr) != codes.Unimplemented {
		t.Fatalf("ListWiFiNetworks code = %s, want Unimplemented", status.Code(rpcErr))
	}

	err := macOSBetaUnsupportedFeatureError(ctx, client, rpcErr, "Wi-Fi network scanning")
	if err == nil {
		t.Fatal("macOSBetaUnsupportedFeatureError returned nil")
	}

	got := err.Error()
	if !strings.Contains(got, "current Wendy Agent for macOS beta") {
		t.Fatalf("error = %q, want macOS beta message", got)
	}
	if strings.Contains(got, "updating") || strings.Contains(got, "agent version") {
		t.Fatalf("error = %q, should not suggest updating the agent", got)
	}
}

func TestMacOSBetaUnsupportedFeatureError_IgnoresNonMacAgents(t *testing.T) {
	ctx := context.Background()
	client := startUnsupportedAgentClient(t, "linux")

	_, rpcErr := client.ListWiFiNetworks(ctx, &agentpb.ListWiFiNetworksRequest{})
	if status.Code(rpcErr) != codes.Unimplemented {
		t.Fatalf("ListWiFiNetworks code = %s, want Unimplemented", status.Code(rpcErr))
	}

	if err := macOSBetaUnsupportedFeatureError(ctx, client, rpcErr, "Wi-Fi network scanning"); err != nil {
		t.Fatalf("macOSBetaUnsupportedFeatureError returned %v, want nil", err)
	}
}

func TestMacOSBetaUnsupportedFeatureError_IgnoresNonUnimplementedErrors(t *testing.T) {
	client := startUnsupportedAgentClient(t, "darwin")

	err := macOSBetaUnsupportedFeatureError(context.Background(), client, errors.New("boom"), "Wi-Fi network scanning")
	if err != nil {
		t.Fatalf("macOSBetaUnsupportedFeatureError returned %v, want nil", err)
	}
}
