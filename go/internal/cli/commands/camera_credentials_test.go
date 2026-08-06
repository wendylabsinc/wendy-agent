package commands

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func writeWendyJSON(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "wendy.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write wendy.json: %v", err)
	}
	return dir
}

func TestCameraCredentialsFromConfig(t *testing.T) {
	dir := writeWendyJSON(t, `{
		"appId": "demo",
		"version": "0.1.0",
		"entitlements": [
			{ "type": "camera", "user": "admin", "password": "hunter2" }
		]
	}`)
	user, password, found := cameraCredentialsFromConfig(dir)
	if !found {
		t.Fatal("credentials in wendy.json were not found")
	}
	if user != "admin" || password != "hunter2" {
		t.Fatalf("got %q/%q, want admin/hunter2", user, password)
	}
}

// A camera entitlement with no credentials is the common case and must not be
// mistaken for a configured login.
func TestCameraCredentialsFromConfigAbsent(t *testing.T) {
	cases := map[string]string{
		"no credentials on entitlement": `{
			"appId": "demo", "version": "0.1.0",
			"entitlements": [{ "type": "camera" }]
		}`,
		"no camera entitlement": `{
			"appId": "demo", "version": "0.1.0",
			"entitlements": [{ "type": "gpu" }]
		}`,
		"no entitlements": `{ "appId": "demo", "version": "0.1.0" }`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, found := cameraCredentialsFromConfig(writeWendyJSON(t, body)); found {
				t.Fatalf("%s was treated as configured credentials", name)
			}
		})
	}

	// A directory with no wendy.json at all is not an error either.
	if _, _, found := cameraCredentialsFromConfig(t.TempDir()); found {
		t.Fatal("a directory with no wendy.json reported credentials")
	}
}

func credentialsNeededError(t *testing.T, deviceID string) error {
	t.Helper()
	st := status.New(codes.FailedPrecondition, "camera has no stored credentials")
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   "IP_CAMERA_NO_CREDENTIALS",
		Metadata: map[string]string{"device_id": deviceID},
	})
	if err != nil {
		t.Fatalf("building error: %v", err)
	}
	return detailed.Err()
}

// Only the agent's machine-readable signal triggers the credential flow, so a
// stream that failed for any other reason is left alone.
func TestCameraNeedsCredentials(t *testing.T) {
	id, ok := cameraNeedsCredentials(credentialsNeededError(t, "203"))
	if !ok {
		t.Fatal("the credentials signal was not recognised")
	}
	if id != 203 {
		t.Fatalf("device id = %d, want 203", id)
	}

	for name, err := range map[string]error{
		"nil":            nil,
		"plain error":    errors.New("boom"),
		"other status":   status.Error(codes.NotFound, "camera 9 not found"),
		"other reason":   func() error { return status.Error(codes.FailedPrecondition, "nope") }(),
		"unparseable id": credentialsNeededError(t, "not-a-number"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := cameraNeedsCredentials(err); ok {
				t.Fatalf("%s was treated as a credentials request", name)
			}
		})
	}
}

// wendy.json wins, and no prompt happens even when one would be possible.
func TestResolveCameraCredentialsPrefersConfig(t *testing.T) {
	dir := writeWendyJSON(t, `{
		"appId": "demo", "version": "0.1.0",
		"entitlements": [{ "type": "camera", "user": "operator", "password": "fromconfig" }]
	}`)
	var got *agentpb.SetCameraCredentialsRequest
	err := resolveCameraCredentials(context.Background(), newCameraTestCmd(),
		func(_ context.Context, r *agentpb.SetCameraCredentialsRequest) error {
			got = r
			return nil
		}, 200, dir, true)
	if err != nil {
		t.Fatalf("resolveCameraCredentials: %v", err)
	}
	if got == nil {
		t.Fatal("credentials were never stored")
	}
	if got.GetUsername() != "operator" || got.GetPassword() != "fromconfig" {
		t.Fatalf("stored %q/%q, want the wendy.json values", got.GetUsername(), got.GetPassword())
	}
	if got.GetDeviceId() != 200 {
		t.Fatalf("device id = %d, want 200", got.GetDeviceId())
	}
}

// With no wendy.json, the environment stands in for a prompt so unattended runs
// still work.
func TestResolveCameraCredentialsFromEnvironment(t *testing.T) {
	t.Setenv("WENDY_CAMERA_PASSWORD", "fromenv")
	var got *agentpb.SetCameraCredentialsRequest
	err := resolveCameraCredentials(context.Background(), newCameraTestCmd(),
		func(_ context.Context, r *agentpb.SetCameraCredentialsRequest) error {
			got = r
			return nil
		}, 201, t.TempDir(), true)
	if err != nil {
		t.Fatalf("resolveCameraCredentials: %v", err)
	}
	if got.GetPassword() != "fromenv" {
		t.Fatalf("password = %q, want the environment value", got.GetPassword())
	}
	if got.GetUsername() != defaultCameraUser {
		t.Fatalf("username = %q, want %q", got.GetUsername(), defaultCameraUser)
	}
}

// Nothing to read and nowhere to prompt must produce an actionable error rather
// than hang waiting on a terminal that is not there.
func TestResolveCameraCredentialsNonInteractive(t *testing.T) {
	err := resolveCameraCredentials(context.Background(), newCameraTestCmd(),
		func(_ context.Context, _ *agentpb.SetCameraCredentialsRequest) error {
			t.Fatal("credentials were stored despite having none")
			return nil
		}, 202, t.TempDir(), false)
	if !errors.Is(err, errNoCameraCredentials) {
		t.Fatalf("err = %v, want errNoCameraCredentials", err)
	}
	for _, want := range []string{"wendy.json", "WENDY_CAMERA_PASSWORD", "camera login 202"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

// A store failure is surfaced, not swallowed, or the retry would fail again with
// a confusing message.
func TestResolveCameraCredentialsStoreFailure(t *testing.T) {
	dir := writeWendyJSON(t, `{
		"appId": "demo", "version": "0.1.0",
		"entitlements": [{ "type": "camera", "user": "admin", "password": "x" }]
	}`)
	sentinel := errors.New("device unreachable")
	err := resolveCameraCredentials(context.Background(), newCameraTestCmd(),
		func(_ context.Context, _ *agentpb.SetCameraCredentialsRequest) error { return sentinel },
		200, dir, true)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the store error", err)
	}
}

// An environment password means no terminal is needed.
func TestCameraPromptAllowedWithEnvironment(t *testing.T) {
	t.Setenv("WENDY_CAMERA_PASSWORD", "x")
	if !cameraPromptAllowed() {
		t.Fatal("an environment password should count as promptable")
	}
}

// newCameraTestCmd returns a command whose output goes nowhere, so prompts in
// tests do not write to the test log.
func newCameraTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	return cmd
}
