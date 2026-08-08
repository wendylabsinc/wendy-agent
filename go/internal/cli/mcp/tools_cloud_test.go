package mcp

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
)

type fakeCloudAssetServer struct {
	cloudpb.UnimplementedAssetServiceServer
	assets []*cloudpb.Asset
	req    *cloudpb.ListAssetsRequest
}

func (s *fakeCloudAssetServer) ListAssets(req *cloudpb.ListAssetsRequest, stream grpc.ServerStreamingServer[cloudpb.ListAssetsResponse]) error {
	s.req = req
	for _, a := range s.assets {
		if err := stream.Send(&cloudpb.ListAssetsResponse{Asset: a}); err != nil {
			return err
		}
	}
	return nil
}

func startFakeCloudAssetServer(t *testing.T, svc *fakeCloudAssetServer) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	g := grpc.NewServer()
	cloudpb.RegisterAssetServiceServer(g, svc)
	go func() { _ = g.Serve(ln) }()
	t.Cleanup(func() { g.Stop() })
	return ln.Addr().String()
}

func TestCloudDiscover_ReturnsConfiguredCloudDevices(t *testing.T) {
	fake := &fakeCloudAssetServer{
		assets: []*cloudpb.Asset{
			{
				Id:              42,
				OrganizationId:  7,
				Name:            "edge-one",
				AssetType:       "device",
				IsComputeDevice: true,
			},
		},
	}
	addr := startFakeCloudAssetServer(t, fake)
	srv := New(&config.Config{
		Auth: []config.AuthConfig{{
			CloudGRPC: addr,
			Certificates: []config.CertificateInfo{{
				OrganizationID: 7,
			}},
		}},
	}, nil)

	result, err := srv.callTool(context.Background(), "cloud_discover", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	devices := listPayload(t, result, "devices")
	if len(devices) != 1 {
		t.Fatalf("len(devices) = %d, want 1", len(devices))
	}
	if devices[0]["name"] != "edge-one" {
		t.Errorf("name = %v, want edge-one", devices[0]["name"])
	}
	if fake.req == nil || fake.req.GetOrganizationId() != 7 {
		t.Fatalf("ListAssets organization_id = %v, want 7", fake.req)
	}
	if !fake.req.GetIsComputeDevice() {
		t.Fatal("ListAssets did not request compute devices")
	}
}

func TestCloudDiscover_HasStructuredContent(t *testing.T) {
	fake := &fakeCloudAssetServer{
		assets: []*cloudpb.Asset{
			{
				Id:              42,
				OrganizationId:  7,
				Name:            "edge-one",
				AssetType:       "device",
				IsComputeDevice: true,
			},
		},
	}
	addr := startFakeCloudAssetServer(t, fake)
	srv := New(&config.Config{
		Auth: []config.AuthConfig{{
			CloudGRPC: addr,
			Certificates: []config.CertificateInfo{{
				OrganizationID: 7,
			}},
		}},
	}, nil)

	result, err := srv.callTool(context.Background(), "cloud_discover", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", result.Content)
	}
	if _, ok := structuredMap(t, result)["devices"]; !ok {
		t.Error("cloud_discover envelope is missing the devices key")
	}
}

func TestCloudDiscover_MaxBytesTruncates(t *testing.T) {
	assets := make([]*cloudpb.Asset, 0, 200)
	for i := int32(0); i < 200; i++ {
		assets = append(assets, &cloudpb.Asset{
			Id:              i,
			OrganizationId:  7,
			Name:            "some-padding-device-name",
			AssetType:       "device",
			IsComputeDevice: true,
		})
	}
	fake := &fakeCloudAssetServer{assets: assets}
	addr := startFakeCloudAssetServer(t, fake)
	srv := New(&config.Config{
		Auth: []config.AuthConfig{{
			CloudGRPC: addr,
			Certificates: []config.CertificateInfo{{
				OrganizationID: 7,
			}},
		}},
	}, nil)

	result, err := srv.callTool(context.Background(), "cloud_discover", map[string]any{"max_bytes": 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("truncation is not an error result: %v", result.Content)
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structuredContent has unexpected type %T", result.StructuredContent)
	}
	if sc["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", sc["truncated"])
	}
}

func TestCloud_MultipleSessions_Code(t *testing.T) {
	srv := New(&config.Config{
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
		},
	}, nil)
	result, err := srv.callTool(context.Background(), "cloud_discover", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
	sc, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map structured content, got %T", result.StructuredContent)
	}
	if sc["error_code"] != string(errCodeMultipleSessions) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeMultipleSessions)
	}
}

func TestCloudDiscover_RequiresAuth(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_discover", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
}

func TestCloudDiscover_RequiresCloudGRPCWhenMultipleAuthSessionsExist(t *testing.T) {
	srv := New(&config.Config{
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
		},
	}, nil)
	result, err := srv.callTool(context.Background(), "cloud_discover", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result")
	}
}

func TestRun_MissingProjectPath(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true when project_path is missing")
	}
}

func TestCloudTunnel_RejectsUnknownProtocol(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_tunnel", map[string]any{
		"local_port": 8080,
		"protocol":   "quic",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for unknown protocol")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeInvalidArgument) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeInvalidArgument)
	}
}

func TestCloudTunnel_DefaultsProtocolToTCP(t *testing.T) {
	// Without a "protocol" argument at all, the invalid-argument short-circuit
	// must not fire — the request should proceed past port/protocol
	// validation and fail later for lack of auth, not for protocol.
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_tunnel", map[string]any{
		"local_port": 8080,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result (no auth configured)")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] == string(errCodeInvalidArgument) {
		t.Errorf("expected the failure to come from auth resolution, not protocol validation; got %v", sc)
	}
}

func TestCloudTunnel_RejectsOutOfRangeLocalPort(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_tunnel", map[string]any{
		"local_port": 70000,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for out-of-range local_port")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeInvalidArgument) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeInvalidArgument)
	}
}

func TestCloudPing_RequiresDeviceName(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_ping", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result when device_name is missing")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeInvalidArgument) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeInvalidArgument)
	}
}

func TestCloudPing_RejectsCountAboveMax(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_ping", map[string]any{
		"device_name": "edge-one",
		"count":       21,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for count above max (20)")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeInvalidArgument) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeInvalidArgument)
	}
}

func TestCloudPing_RejectsCountBelowMin(t *testing.T) {
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_ping", map[string]any{
		"device_name": "edge-one",
		"count":       0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for count below min (1)")
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeInvalidArgument) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeInvalidArgument)
	}
}

func TestCloudPing_DefaultCountWithinBounds(t *testing.T) {
	// No count argument at all should use the default (4), which is within
	// bounds — the failure here must come from auth resolution, not count
	// validation.
	srv := New(&config.Config{}, nil)
	result, err := srv.callTool(context.Background(), "cloud_ping", map[string]any{
		"device_name": "edge-one",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result (no auth configured)")
	}
	sc := structuredMap(t, result)
	if sc["message"] == "count must be between 1 and 20" {
		t.Errorf("default count should not trigger count validation; got %v", sc)
	}
}

func TestCloudAuthEntry_UsesDefaultWhenMultiple(t *testing.T) {
	srv := New(&config.Config{
		DefaultCloudGRPC: "two:123",
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 2}}},
		},
	}, nil)
	auth, err := srv.cloudAuthEntry("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth.CloudGRPC != "two:123" {
		t.Fatalf("want default session two:123, got %s", auth.CloudGRPC)
	}
}

func TestCloudAuthEntry_ErrorsMentionsCloudGRPCParam(t *testing.T) {
	srv := New(&config.Config{
		Auth: []config.AuthConfig{
			{CloudGRPC: "one:123", Certificates: []config.CertificateInfo{{OrganizationID: 1}}},
			{CloudGRPC: "two:123", Certificates: []config.CertificateInfo{{OrganizationID: 2}}},
		},
	}, nil)
	_, err := srv.cloudAuthEntry("")
	if err == nil || !strings.Contains(err.Error(), "cloud_grpc") {
		t.Fatalf("want error mentioning cloud_grpc param, got %v", err)
	}
}
