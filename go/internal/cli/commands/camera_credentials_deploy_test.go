package commands

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeDeployCameraCredentialsClient is a struct fake for
// deployCameraCredentialsClient. It records every SetCameraCredentials call so
// tests can assert on push behavior (which cameras, in what order) without a
// real gRPC connection.
type fakeDeployCameraCredentialsClient struct {
	devices []*agentpb.VideoDevice
	listErr error

	// setErr, keyed by device ID, lets a test fail one camera's Set without
	// affecting the others.
	setErr map[uint32]error

	listCalls int
	setCalls  []*agentpb.SetCameraCredentialsRequest
}

func (f *fakeDeployCameraCredentialsClient) ListVideoDevices(context.Context, *agentpb.ListVideoDevicesRequest, ...grpc.CallOption) (*agentpb.ListVideoDevicesResponse, error) {
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return &agentpb.ListVideoDevicesResponse{Devices: f.devices}, nil
}

func (f *fakeDeployCameraCredentialsClient) SetCameraCredentials(_ context.Context, req *agentpb.SetCameraCredentialsRequest, _ ...grpc.CallOption) (*agentpb.SetCameraCredentialsResponse, error) {
	f.setCalls = append(f.setCalls, req)
	if err := f.setErr[req.GetDeviceId()]; err != nil {
		return nil, err
	}
	return &agentpb.SetCameraCredentialsResponse{}, nil
}

// appConfigWithCameraCredentials builds a minimal AppConfig carrying a single
// camera entitlement, the shape pushCameraCredentialsForDeploy reads.
func appConfigWithCameraCredentials(user, password string) *appconfig.AppConfig {
	return &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementCamera, User: user, Password: password},
		},
	}
}

func TestCameraCredentialsFromAppConfig(t *testing.T) {
	tests := []struct {
		name         string
		entitlements []appconfig.Entitlement
		services     map[string]*appconfig.ServiceConfig
		wantUser     string
		wantPassword string
		wantFound    bool
	}{
		{
			name: "camera entitlement with user and password",
			entitlements: []appconfig.Entitlement{
				{Type: appconfig.EntitlementCamera, User: "admin", Password: "hunter2"},
			},
			wantUser:     "admin",
			wantPassword: "hunter2",
			wantFound:    true,
		},
		{
			name: "video entitlement with user and password",
			entitlements: []appconfig.Entitlement{
				{Type: appconfig.EntitlementVideo, User: "admin", Password: "hunter2"},
			},
			wantUser:     "admin",
			wantPassword: "hunter2",
			wantFound:    true,
		},
		{
			// Locks current semantics: a password with no username is not a
			// configured login. (A vendor default user is assumed only by the
			// interactive prompt path, never inferred here.)
			name: "password with empty user",
			entitlements: []appconfig.Entitlement{
				{Type: appconfig.EntitlementCamera, Password: "hunter2"},
			},
			wantFound: false,
		},
		{
			name:      "no entitlements",
			wantFound: false,
		},
		{
			// Multi-service apps can carry the camera entitlement only under a
			// service (appconfig.go ServiceConfig.Entitlements), never at the
			// top level. multiServiceCreateConfig (multibuild.go:842) merges
			// it into that service's effective container config, so the
			// deploy-time push must see it too.
			name: "camera entitlement only under a service",
			services: map[string]*appconfig.ServiceConfig{
				"camera-worker": {
					Entitlements: []appconfig.Entitlement{
						{Type: appconfig.EntitlementCamera, User: "svcuser", Password: "svcpass"},
					},
				},
			},
			wantUser:     "svcuser",
			wantPassword: "svcpass",
			wantFound:    true,
		},
		{
			// multiServiceCreateConfig merges top-level entitlements BEFORE a
			// service's own (appCfg.Entitlements then svc.Entitlements), so a
			// top-level camera credential must win a conflict the same way.
			name: "top-level credential takes precedence over a service's",
			entitlements: []appconfig.Entitlement{
				{Type: appconfig.EntitlementCamera, User: "topuser", Password: "toppass"},
			},
			services: map[string]*appconfig.ServiceConfig{
				"camera-worker": {
					Entitlements: []appconfig.Entitlement{
						{Type: appconfig.EntitlementCamera, User: "svcuser", Password: "svcpass"},
					},
				},
			},
			wantUser:     "topuser",
			wantPassword: "toppass",
			wantFound:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &appconfig.AppConfig{Entitlements: tc.entitlements, Services: tc.services}
			user, password, found := cameraCredentialsFromAppConfig(cfg)
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !tc.wantFound {
				return
			}
			if user != tc.wantUser || password != tc.wantPassword {
				t.Fatalf("got %q/%q, want %q/%q", user, password, tc.wantUser, tc.wantPassword)
			}
		})
	}
}

// The device, not the entitlement, knows which cameras need a login: push to
// every registered IP camera reporting has_credentials == false, skip IP
// cameras that already have a login (never overwrite), and skip non-IP
// (local) cameras entirely.
func TestPushCameraCredentialsForDeploy_StoresForCredentiallessIPCameras(t *testing.T) {
	client := &fakeDeployCameraCredentialsClient{
		devices: []*agentpb.VideoDevice{
			{Id: 203, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: false},
			{Id: 204, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: true},
			{Id: 0, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_USB, HasCredentials: false},
		},
	}
	appCfg := appConfigWithCameraCredentials("admin", "hunter2")

	pushCameraCredentialsForDeploy(context.Background(), client, appCfg)

	if len(client.setCalls) != 1 {
		t.Fatalf("SetCameraCredentials called %d times, want 1: %+v", len(client.setCalls), client.setCalls)
	}
	got := client.setCalls[0]
	if got.GetDeviceId() != 203 {
		t.Fatalf("pushed to device %d, want 203", got.GetDeviceId())
	}
	if got.GetUsername() != "admin" || got.GetPassword() != "hunter2" {
		t.Fatalf("pushed %q/%q, want admin/hunter2", got.GetUsername(), got.GetPassword())
	}
}

// No camera entitlement in wendy.json means nothing to push: the device is
// never even asked to list cameras.
func TestPushCameraCredentialsForDeploy_NoConfiguredCredentials_MakesNoRPCs(t *testing.T) {
	client := &fakeDeployCameraCredentialsClient{
		devices: []*agentpb.VideoDevice{
			{Id: 203, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: false},
		},
	}
	appCfg := &appconfig.AppConfig{}

	pushCameraCredentialsForDeploy(context.Background(), client, appCfg)

	if client.listCalls != 0 {
		t.Fatalf("ListVideoDevices called %d times, want 0", client.listCalls)
	}
	if len(client.setCalls) != 0 {
		t.Fatalf("SetCameraCredentials called %d times, want 0", len(client.setCalls))
	}
}

// Unavailable is what an agent with no camera registry wired up returns
// (video_service.go: registry == nil), and Unimplemented is what an agent
// built before this RPC existed returns. Both mean "this device does not do
// network cameras" rather than a problem to report, so the push must stay
// silent — no cliNotice — and let the deploy continue.
func TestPushCameraCredentialsForDeploy_ListUnavailableIsSilentSkip(t *testing.T) {
	client := &fakeDeployCameraCredentialsClient{
		listErr: status.Error(codes.Unavailable, "network camera support unavailable"),
	}
	appCfg := appConfigWithCameraCredentials("admin", "hunter2")

	_, stderr := captureBoth(t, func() {
		pushCameraCredentialsForDeploy(context.Background(), client, appCfg)
	})

	if stderr != "" {
		t.Fatalf("stderr = %q, want silence for an Unavailable list error", stderr)
	}
	if len(client.setCalls) != 0 {
		t.Fatalf("SetCameraCredentials called %d times, want 0", len(client.setCalls))
	}
}

// A camera forgotten between list and set (NotFound) must not stop the push
// for cameras still registered: one bad apple can't cost the rest of the
// fleet its credentials.
func TestPushCameraCredentialsForDeploy_SetErrorContinuesToNextCamera(t *testing.T) {
	client := &fakeDeployCameraCredentialsClient{
		devices: []*agentpb.VideoDevice{
			{Id: 203, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: false},
			{Id: 205, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: false},
		},
		setErr: map[uint32]error{
			203: status.Error(codes.NotFound, "camera 203 not found"),
		},
	}
	appCfg := appConfigWithCameraCredentials("admin", "hunter2")

	pushCameraCredentialsForDeploy(context.Background(), client, appCfg)

	if len(client.setCalls) != 2 {
		t.Fatalf("SetCameraCredentials called %d times, want 2: %+v", len(client.setCalls), client.setCalls)
	}
	if client.setCalls[0].GetDeviceId() != 203 || client.setCalls[1].GetDeviceId() != 205 {
		t.Fatalf("pushed to devices %d, %d, want 203, 205", client.setCalls[0].GetDeviceId(), client.setCalls[1].GetDeviceId())
	}
}

// The credential store is MAC-keyed, not reachability-gated (video_service.go
// SetCameraCredentials has no online check), so an offline IP camera still
// gets its login stored — it may come back online later, or another agent
// process may reach it over a path this probe didn't.
func TestPushCameraCredentialsForDeploy_PushesToOfflineCameras(t *testing.T) {
	client := &fakeDeployCameraCredentialsClient{
		devices: []*agentpb.VideoDevice{
			{Id: 206, Transport: agentpb.VideoTransport_VIDEO_TRANSPORT_IP, HasCredentials: false, Online: false},
		},
	}
	appCfg := appConfigWithCameraCredentials("admin", "hunter2")

	pushCameraCredentialsForDeploy(context.Background(), client, appCfg)

	if len(client.setCalls) != 1 || client.setCalls[0].GetDeviceId() != 206 {
		t.Fatalf("setCalls = %+v, want exactly one call for device 206", client.setCalls)
	}
}
