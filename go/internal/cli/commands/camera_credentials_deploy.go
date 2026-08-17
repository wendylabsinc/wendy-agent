package commands

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// deployCameraCredentialsClient is the narrow slice of
// agentpb.WendyVideoServiceClient the deploy-time push needs (same pattern as
// cameraTester in camera.go:235).
type deployCameraCredentialsClient interface {
	ListVideoDevices(ctx context.Context, in *agentpb.ListVideoDevicesRequest, opts ...grpc.CallOption) (*agentpb.ListVideoDevicesResponse, error)
	SetCameraCredentials(ctx context.Context, in *agentpb.SetCameraCredentialsRequest, opts ...grpc.CallOption) (*agentpb.SetCameraCredentialsResponse, error)
}

// deployCameraCredentialsPushTimeout bounds the entire push — the list call
// plus every SetCameraCredentials call it fans out to — so a wedged RPC
// cannot stall a deploy for what is, at worst, a convenience.
const deployCameraCredentialsPushTimeout = 15 * time.Second

// pushCameraCredentialsForDeploy stores wendy.json camera-entitlement
// credentials on the device for every registered IP camera that has none.
// Best-effort by design: no return value, deploy always proceeds. Returns
// immediately with zero RPCs when the config carries no camera credentials.
//
// Target resolution: the entitlement itself carries no camera identity, only
// a user/password — appconfig's `allowlist` field names /dev/videoN paths,
// which network cameras don't have. The device is the one that knows which
// cameras need a login, via ListVideoDevices' transport and has_credentials
// fields, so that drives selection: every VIDEO_TRANSPORT_IP device reporting
// has_credentials == false gets this same login. That is correct for both a
// single-camera fleet and a fleet of cameras sharing one admin password —
// the two shapes this exists to serve — and a wrong push is recoverable,
// since `camera test` reports AUTH_FAILED with a login hint.
//
// Never overwrites: a camera with has_credentials == true is skipped, so a
// redeploy is idempotent and can never clobber a credential fixed by hand via
// `camera login`. One consequence worth documenting (see the embedded docs):
// rotating the password in wendy.json alone does not update a camera that
// already has stored credentials — that needs `camera login` or `camera
// forget` + redeploy.
func pushCameraCredentialsForDeploy(ctx context.Context, video deployCameraCredentialsClient, appCfg *appconfig.AppConfig) {
	user, password, found := cameraCredentialsFromAppConfig(appCfg)
	if !found {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, deployCameraCredentialsPushTimeout)
	defer cancel()

	resp, err := video.ListVideoDevices(ctx, &agentpb.ListVideoDevicesRequest{})
	if err != nil {
		// Unimplemented: an agent built before this RPC existed. Unavailable:
		// a darwin agent, or a Linux agent with no camera registry wired up
		// (video_service.go returns Unavailable when registry == nil). Both
		// mean "this device does not do network cameras", not a problem to
		// report, so stay silent rather than nag every deploy.
		switch status.Code(err) {
		case codes.Unimplemented, codes.Unavailable:
			return
		}
		cliNotice("Could not list cameras to push wendy.json credentials (%v); run `wendy device camera login <id>` on any camera that needs one.", err)
		return
	}

	for _, dev := range resp.GetDevices() {
		if dev.GetTransport() != agentpb.VideoTransport_VIDEO_TRANSPORT_IP || dev.GetHasCredentials() {
			continue
		}
		if _, err := video.SetCameraCredentials(ctx, &agentpb.SetCameraCredentialsRequest{
			DeviceId: dev.GetId(),
			Username: user,
			Password: password,
		}); err != nil {
			// e.g. NotFound for a camera forgotten between list and set. One
			// camera's hiccup must not cost the rest of the fleet its
			// credentials — matches camera view's degradation-first pattern.
			cliNotice("Could not store wendy.json credentials for camera %d (%v); run `wendy device camera login %d`.", dev.GetId(), err, dev.GetId())
			continue
		}
	}
}
