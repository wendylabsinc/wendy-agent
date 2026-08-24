package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	"github.com/wendylabsinc/wendy/go/internal/shared/streamreason"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// Credentials for a network camera are resolved through two triggers, not
// one:
//
//  1. A proactive push at deploy time (pushCameraCredentialsForDeploy in
//     camera_credentials_deploy.go): every registered IP camera with no
//     stored login gets wendy.json's camera entitlement before the app that
//     wants it ever asks.
//  2. The reactive path below, scoped to one camera at a time and driven by
//     the agent reporting IP_CAMERA_NO_CREDENTIALS for it:
//     a. wendy.json, so an unattended deploy needs no prompt.
//     b. an interactive prompt, so a developer with no wendy.json is not stuck.
//
// Local cameras never reach either path: nothing asks a USB webcam to log in.

// cameraCredentialsFromConfig reads camera credentials out of wendy.json in dir.
// Missing file, missing entitlement and missing fields are all "not configured"
// rather than errors: this is an optional convenience.
func cameraCredentialsFromConfig(dir string) (user, password string, found bool) {
	cfg, err := appconfig.LoadFromFile(filepath.Join(dir, "wendy.json"))
	if err != nil {
		return "", "", false
	}
	return cameraCredentialsFromAppConfig(cfg)
}

// cameraCredentialsFromAppConfig extracts credentials from an already-loaded
// config; cameraCredentialsFromConfig (path-based, above) delegates here. This
// is the shared rule for both the reactive prompt path and the deploy-time
// push: the entitlement must be camera/video type with a non-empty User. A
// password with no username does not count — the interactive prompt path is
// the only place a vendor-default user is assumed.
//
// A multi-service app can carry the camera entitlement under
// services.<name>.entitlements (appconfig.go ServiceConfig.Entitlements)
// instead of, or in addition to, the top level: multiServiceCreateConfig
// (multibuild.go:842) builds each service's effective container config by
// appending that service's own entitlements onto the shared top-level set —
// appCfg.Entitlements first, svc.Entitlements after — so a top-level
// candidate always wins a same-type conflict there. Mirror that precedence
// here: check the top level first, then fall back to each service's own
// entitlements in sorted-name order (a fixed order so the result can't
// depend on Go's randomized map iteration), and return on the first match.
func cameraCredentialsFromAppConfig(cfg *appconfig.AppConfig) (user, password string, found bool) {
	if user, password, found = cameraCredentialFromEntitlements(cfg.Entitlements); found {
		return user, password, found
	}

	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := cfg.Services[name]
		if svc == nil {
			continue
		}
		if user, password, found = cameraCredentialFromEntitlements(svc.Entitlements); found {
			return user, password, found
		}
	}
	return "", "", false
}

// cameraCredentialFromEntitlements returns the first camera/video
// entitlement in ents that carries a non-empty User.
func cameraCredentialFromEntitlements(ents []appconfig.Entitlement) (user, password string, found bool) {
	for _, ent := range ents {
		if ent.Type != appconfig.EntitlementCamera && ent.Type != appconfig.EntitlementVideo {
			continue
		}
		if ent.User == "" {
			continue
		}
		return ent.User, ent.Password, true
	}
	return "", "", false
}

// cameraNeedsCredentials reports the device ID a failed request wants a login for.
// It is the agent's machine-readable signal, so a stream that failed for any other
// reason is left alone.
func cameraNeedsCredentials(err error) (uint32, bool) {
	info := streamreason.Info(err)
	if info == nil || info.GetReason() != streamreason.IPCameraNoCredentials {
		return 0, false
	}
	id, parseErr := parseCameraID(info.GetMetadata()["device_id"])
	if parseErr != nil {
		return 0, false
	}
	return id, true
}

// streamVideoWithCredentialRetry starts a camera stream and resolves a missing
// network-camera login exactly once, whether gRPC reports it while constructing
// the server stream or on the first Recv call. grpc-go normally uses the latter
// path for server-handler status errors.
func streamVideoWithCredentialRetry(
	start func() (videoStream, error),
	resolve func(deviceID uint32) error,
) (videoStream, error) {
	stream, err := start()
	attempted := false
	if deviceID, ok := cameraNeedsCredentials(err); ok {
		attempted = true
		if err := resolve(deviceID); err != nil {
			return nil, err
		}
		stream, err = start()
	}
	if err != nil {
		return nil, err
	}
	return &cameraCredentialRetryStream{
		stream:    stream,
		start:     start,
		resolve:   resolve,
		attempted: attempted,
	}, nil
}

// cameraCredentialRetryStream intercepts the first receive-side credentials
// error. Once a retry has been attempted, every result passes through unchanged
// so a bad login cannot trigger an infinite prompt loop.
type cameraCredentialRetryStream struct {
	stream    videoStream
	start     func() (videoStream, error)
	resolve   func(deviceID uint32) error
	attempted bool
}

func (s *cameraCredentialRetryStream) Recv() (*agentpb.VideoFrame, error) {
	frame, err := s.stream.Recv()
	if s.attempted {
		return frame, err
	}
	deviceID, ok := cameraNeedsCredentials(err)
	if !ok {
		return frame, err
	}

	s.attempted = true
	if err := s.resolve(deviceID); err != nil {
		return nil, err
	}
	next, err := s.start()
	if err != nil {
		return nil, err
	}
	s.stream = next
	return s.stream.Recv()
}

// errNoCameraCredentials is returned when neither wendy.json nor a terminal can
// supply a login.
var errNoCameraCredentials = errors.New("no camera credentials available")

// resolveCameraCredentials supplies a login for deviceID, from wendy.json if it
// has one and from a prompt otherwise, and stores it on the device.
//
// promptAllowed is false for non-interactive runs, where prompting would hang.
func resolveCameraCredentials(
	ctx context.Context,
	cmd *cobra.Command,
	set func(context.Context, *agentpb.SetCameraCredentialsRequest) error,
	deviceID uint32,
	dir string,
	promptAllowed bool,
) error {
	user, password, found := cameraCredentialsFromConfig(dir)
	switch {
	case found:
		cliLogln("Using camera credentials from wendy.json for camera %d.", deviceID)
	case promptAllowed:
		user = defaultCameraUser
		cliLogln("Camera %d needs a login.", deviceID)
		secret, err := readCameraPassword(cmd, deviceID)
		if err != nil {
			return err
		}
		password = secret
	default:
		return fmt.Errorf(
			"%w: add a camera entitlement with user and password to wendy.json, "+
				"set WENDY_CAMERA_PASSWORD, or run `wendy device camera login %d`",
			errNoCameraCredentials, deviceID)
	}

	if err := set(ctx, &agentpb.SetCameraCredentialsRequest{
		DeviceId: deviceID,
		Username: user,
		Password: password,
	}); err != nil {
		return fmt.Errorf("storing camera credentials: %w", err)
	}
	return nil
}

// defaultCameraUser is the username assumed when only a password is supplied.
// Every IP camera vendor ships "admin" as the administrative account.
const defaultCameraUser = "admin"

// cameraPromptAllowed reports whether a password can be obtained without
// blocking. An environment-supplied password counts, since it needs no prompt.
func cameraPromptAllowed() bool {
	if os.Getenv("WENDY_CAMERA_PASSWORD") != "" {
		return true
	}
	return term.IsTerminal(int(os.Stdin.Fd()))
}
