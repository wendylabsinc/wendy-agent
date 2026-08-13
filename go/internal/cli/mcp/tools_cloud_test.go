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
	// offlineAssets is served instead of assets when the request has
	// OnlineOnly unset/false, modeling the offline-device re-query pickCloudAsset
	// performs when the online-only lookup finds no match.
	offlineAssets []*cloudpb.Asset
	req           *cloudpb.ListAssetsRequest   // last request, kept for existing single-request assertions
	reqs          []*cloudpb.ListAssetsRequest // every request received, in order
}

func (s *fakeCloudAssetServer) ListAssets(req *cloudpb.ListAssetsRequest, stream grpc.ServerStreamingServer[cloudpb.ListAssetsResponse]) error {
	s.req = req
	s.reqs = append(s.reqs, req)
	assets := s.assets
	if !req.GetOnlineOnly() {
		assets = s.offlineAssets
	}
	for _, a := range assets {
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

// TestCloudConnect_OfflineDeviceReportedOffline covers pickCloudAsset's
// "empty-with-name" path: the online-only listing comes back empty, but the
// device turns up in the unfiltered re-query, so the error should say it's
// enrolled-but-offline (DEVICE_UNREACHABLE) rather than claiming the device
// doesn't exist. It must also never reach a broker dial: connectToCloudAgent
// only calls DialBroker after pickCloudAsset succeeds, and a bogus/loopback
// CloudGRPC address here would surface as a different failure if that
// ordering regressed.
func TestCloudConnect_OfflineDeviceReportedOffline(t *testing.T) {
	fake := &fakeCloudAssetServer{
		offlineAssets: []*cloudpb.Asset{
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

	result, err := srv.callTool(context.Background(), "cloud_connect", map[string]any{"device_name": "edge-one"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got: %v", result.Content)
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeDeviceUnreachable) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeDeviceUnreachable)
	}
	msg, _ := sc["message"].(string)
	if !strings.Contains(msg, "enrolled but currently reported offline") {
		t.Errorf("message = %q, want it to contain %q", msg, "enrolled but currently reported offline")
	}
	if len(fake.reqs) != 2 {
		t.Fatalf("len(fake.reqs) = %d, want 2", len(fake.reqs))
	}
	if !fake.reqs[0].GetOnlineOnly() {
		t.Errorf("reqs[0].OnlineOnly = %v, want true", fake.reqs[0].GetOnlineOnly())
	}
	if fake.reqs[1].GetOnlineOnly() {
		t.Errorf("reqs[1].OnlineOnly = %v, want unset/false", fake.reqs[1].GetOnlineOnly())
	}
}

// TestCloudConnect_UnknownDeviceStaysNotFound covers pickCloudAsset's
// "no-match" path: the online-only listing is non-empty but doesn't contain
// the requested name, and the unfiltered re-query doesn't find it either
// (it isn't enrolled at all), so the original NOT_FOUND message must be
// preserved rather than being replaced with the offline message.
func TestCloudConnect_UnknownDeviceStaysNotFound(t *testing.T) {
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

	result, err := srv.callTool(context.Background(), "cloud_connect", map[string]any{"device_name": "ghost-device"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected error result, got: %v", result.Content)
	}
	sc := structuredMap(t, result)
	if sc["error_code"] != string(errCodeNotFound) {
		t.Errorf("error_code = %v, want %s", sc["error_code"], errCodeNotFound)
	}
	msg, _ := sc["message"].(string)
	if !strings.Contains(msg, `no device named "ghost-device" found`) {
		t.Errorf("message = %q, want it to contain %s", msg, `no device named "ghost-device" found`)
	}
	if strings.Contains(msg, "offline") {
		t.Errorf("message = %q, should not mention offline for a device that was never enrolled", msg)
	}
}
