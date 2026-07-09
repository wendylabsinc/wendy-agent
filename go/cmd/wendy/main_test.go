package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type capturedEvent struct {
	event string
	props map[string]string
}

func captureAnalytics(t *testing.T) *[]capturedEvent {
	t.Helper()
	var events []capturedEvent
	analytics.SetTrackHookForTesting(func(event string, props map[string]string) {
		copied := make(map[string]string, len(props))
		for k, v := range props {
			copied[k] = v
		}
		events = append(events, capturedEvent{event: event, props: copied})
	})
	t.Cleanup(func() { analytics.SetTrackHookForTesting(nil) })
	return &events
}

func newTestRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "wendy",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("device", "", "target device")

	run := &cobra.Command{
		Use:  "run [path]",
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}

	device := &cobra.Command{Use: "device"}
	wifi := &cobra.Command{Use: "wifi"}
	connect := &cobra.Command{
		Use:  "connect SSID",
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	wifi.AddCommand(connect)
	device.AddCommand(wifi)

	failing := &cobra.Command{
		Use: "failing",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errors.New("boom")
		},
	}

	root.AddCommand(run, device, failing)
	return root
}

func runWithArgs(t *testing.T, args []string) (*cobra.Command, error) {
	t.Helper()
	originalArgs := os.Args
	t.Cleanup(func() { os.Args = originalArgs })

	os.Args = args
	root := newTestRoot()
	root.SetArgs(args[1:])
	return root.ExecuteC()
}

// clearCIEnv neutralizes every CI env var so tests run as a "real user".
// Without this, running the suite under GitHub Actions or any other CI
// would silently disable analytics and make the hook-based assertions
// in the rest of the file fail or no-op.
func clearCIEnv(t *testing.T) {
	t.Helper()
	for _, key := range env.CIEnvVars {
		t.Setenv(key, "")
	}
}

func TestTrackCommand_TopLevelSubcommand(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "run", "myfile.json"})
	trackCommand(executed, err, 42*time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d: %+v", len(*events), *events)
	}
	got := (*events)[0]
	if got.event != "command_executed" {
		t.Errorf("event = %q, want %q", got.event, "command_executed")
	}
	if got.props["command_name"] != "wendy run" {
		t.Errorf("command_name = %q, want %q", got.props["command_name"], "wendy run")
	}
	if got.props["command_root"] != "run" {
		t.Errorf("command_root = %q, want %q", got.props["command_root"], "run")
	}
	if got.props["success"] != "true" {
		t.Errorf("success = %q, want %q", got.props["success"], "true")
	}
	if got.props["duration_ms"] != "42" {
		t.Errorf("duration_ms = %q, want %q", got.props["duration_ms"], "42")
	}
	if _, present := got.props["error_class"]; present {
		t.Errorf("error_class must be absent on success, got %q", got.props["error_class"])
	}
}

func TestTrackCommand_NestedSubcommand(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "device", "wifi", "connect", "MySSID"})
	trackCommand(executed, err, time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	got := (*events)[0].props
	if got["command_name"] != "wendy device wifi connect" {
		t.Errorf("command_name = %q, want %q", got["command_name"], "wendy device wifi connect")
	}
	if got["command_root"] != "device" {
		t.Errorf("command_root = %q, want %q", got["command_root"], "device")
	}
}

func TestTrackCommand_StripsFlagAndPositionalValues(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "run", "--device", "secret-host.example.com", "/private/path/file.json"})
	trackCommand(executed, err, time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	got := (*events)[0].props
	for prop, want := range map[string]string{
		"command_name": "wendy run",
		"command_root": "run",
	} {
		if got[prop] != want {
			t.Errorf("%s = %q, want %q", prop, got[prop], want)
		}
	}
	leakage := []string{"secret-host", "private", "file.json", "--device"}
	for _, value := range got {
		for _, leak := range leakage {
			if strings.Contains(value, leak) {
				t.Errorf("event property %q leaked: %q", value, leak)
			}
		}
	}
}

func TestTrackCommand_SingleEventOnFailureWithErrorClass(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "failing"})
	if err == nil {
		t.Fatal("expected runWithArgs to return an error")
	}
	trackCommand(executed, err, time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected exactly 1 event on failure (no separate command_error), got %d: %+v", len(*events), *events)
	}
	got := (*events)[0]
	if got.event != "command_executed" {
		t.Errorf("event = %q, want %q", got.event, "command_executed")
	}
	if got.props["success"] != "false" {
		t.Errorf("success = %q, want %q", got.props["success"], "false")
	}
	if got.props["error_class"] != "other" {
		t.Errorf("error_class = %q, want %q", got.props["error_class"], "other")
	}
	if got.props["command_name"] != "wendy failing" {
		t.Errorf("command_name = %q, want %q", got.props["command_name"], "wendy failing")
	}
}

func TestTrackCommand_RootOnlyInvocation(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy"})
	trackCommand(executed, err, time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected exactly 1 event for a bare invocation, got %d", len(*events))
	}
	got := (*events)[0].props
	if got["command_name"] != "wendy" {
		t.Errorf("command_name = %q, want %q", got["command_name"], "wendy")
	}
	if got["command_root"] != "wendy" {
		t.Errorf("command_root = %q, want %q", got["command_root"], "wendy")
	}
}

func TestCommandRoot_DepthAndNilCases(t *testing.T) {
	root := newTestRoot()
	device, _, err := root.Find([]string{"device"})
	if err != nil || device == nil {
		t.Fatalf("setup: find device subcommand: %v", err)
	}
	wifi, _, err := root.Find([]string{"device", "wifi"})
	if err != nil || wifi == nil {
		t.Fatalf("setup: find wifi subcommand: %v", err)
	}
	connect, _, err := root.Find([]string{"device", "wifi", "connect"})
	if err != nil || connect == nil {
		t.Fatalf("setup: find connect subcommand: %v", err)
	}

	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		want string
	}{
		{"nil", nil, ""},
		{"depth0_root", root, "wendy"},
		{"depth1_device", device, "device"},
		{"depth2_wifi", wifi, "device"},
		{"depth3_connect", connect, "device"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandRoot(tc.cmd); got != tc.want {
				t.Errorf("commandRoot = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrackCommand_NilCommandIsNoOp(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	trackCommand(nil, nil, time.Millisecond)
	if len(*events) != 0 {
		t.Fatalf("expected no events for nil cmd, got %d", len(*events))
	}
}

func TestTrackCommand_IsDevBuildReflectsVersion(t *testing.T) {
	clearCIEnv(t)
	originalVersion := version.Version
	t.Cleanup(func() { version.Version = originalVersion })

	for _, tc := range []struct {
		ver  string
		want string
	}{
		{"dev", "true"},
		{"2026.04.27-103045-dev", "true"},
		{"2026.04.27-103045", "false"},
		{"v0.10.0", "false"},
	} {
		t.Run(tc.ver, func(t *testing.T) {
			version.Version = tc.ver
			events := captureAnalytics(t)
			executed, err := runWithArgs(t, []string{"wendy", "run"})
			trackCommand(executed, err, time.Millisecond)

			if len(*events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(*events))
			}
			if got := (*events)[0].props["is_dev_build"]; got != tc.want {
				t.Errorf("is_dev_build = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrackCommand_DurationIsSerializedMilliseconds(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "run"})
	trackCommand(executed, err, 1234*time.Millisecond)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	got := (*events)[0].props["duration_ms"]
	if _, parseErr := strconv.ParseInt(got, 10, 64); parseErr != nil {
		t.Errorf("duration_ms = %q, must be parseable int64: %v", got, parseErr)
	}
	if got != "1234" {
		t.Errorf("duration_ms = %q, want %q", got, "1234")
	}
}

func TestErrorClass_Mapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"user_cancelled", commands.ErrUserCancelled, "user_cancelled"},
		{"default_cleared", commands.ErrDefaultCleared, "user_cancelled"},
		{"user_cancelled_wrapped", fmt.Errorf("aborted: %w", commands.ErrUserCancelled), "user_cancelled"},
		{"context_canceled", context.Canceled, "context_canceled"},
		{"context_deadline", context.DeadlineExceeded, "context_deadline"},
		{"grpc_canceled_status", status.Error(codes.Canceled, "client gone"), "context_canceled"},
		{"grpc_unavailable_status", status.Error(codes.Unavailable, "transport closing"), "grpc_unavailable"},
		{"grpc_unavailable_wrapped", fmt.Errorf("connecting to cloud: %w", status.Error(codes.Unavailable, "x")), "grpc_unavailable"},
		{"grpc_deadline_status", status.Error(codes.DeadlineExceeded, "ctx done"), "grpc_deadline"},
		{"grpc_unimplemented_status", status.Error(codes.Unimplemented, "nope"), "grpc_unimplemented"},
		{"grpc_internal", status.Error(codes.Internal, "boom"), "grpc_other"},
		{"grpc_unknown_explicit", status.Error(codes.Unknown, "vague"), "grpc_other"},
		{"non_grpc", errors.New("some plain failure"), "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorClass(tc.err); got != tc.want {
				t.Errorf("errorClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestErrorClass_NeverLeaksMessageText(t *testing.T) {
	// Even when the error embeds sensitive substrings, only the bounded
	// enum value is returned.
	leaky := status.Error(codes.Unavailable, "could not reach secret-host.example.com")
	got := errorClass(leaky)
	if got != "grpc_unavailable" {
		t.Errorf("errorClass = %q, want %q", got, "grpc_unavailable")
	}
	if strings.Contains(got, "secret-host") {
		t.Errorf("errorClass leaked sensitive text: %q", got)
	}
}

func TestErrorClass_ReasonError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"install_download", commands.WithReason("install_download", errors.New("boom")), "install_download"},
		{"install_disk_write", commands.WithReason("install_disk_write", errors.New("io error")), "install_disk_write"},
		{"wrapped_reason", fmt.Errorf("outer: %w", commands.WithReason("install_manifest", errors.New("x"))), "install_manifest"},
		// Cancellation and deadline must win over a reason wrap.
		{"reason_wrapping_cancel", commands.WithReason("install_disk_write", context.Canceled), "context_canceled"},
		{"reason_wrapping_deadline", commands.WithReason("install_download", context.DeadlineExceeded), "context_deadline"},
		{"reason_wrapping_user_cancel", commands.WithReason("install_disk_write", commands.ErrUserCancelled), "user_cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorClass(tc.err); got != tc.want {
				t.Errorf("errorClass = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrackCommand_ErrorDetailForCatchAll(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	// The "failing" command returns errors.New("boom") → class "other".
	executed, err := runWithArgs(t, []string{"wendy", "failing"})
	if err == nil {
		t.Fatal("expected an error")
	}
	trackCommand(executed, err, time.Millisecond)

	got := (*events)[0].props
	if got["error_class"] != "other" {
		t.Fatalf("error_class = %q, want %q", got["error_class"], "other")
	}
	if got["error_detail"] != "boom" {
		t.Errorf("error_detail = %q, want %q", got["error_detail"], "boom")
	}
}

func TestTrackCommand_ErrorDetailRedactsPII(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	root := newTestRoot()
	executed, _, err := root.Find([]string{"failing"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	leaky := errors.New("writing image: /dev/disk4: dial 192.168.1.10 failed for host secret.example.com")
	trackCommand(executed, leaky, time.Millisecond)

	detail := (*events)[0].props["error_detail"]
	for _, leak := range []string{"/dev/disk4", "192.168.1.10", "secret.example.com"} {
		if strings.Contains(detail, leak) {
			t.Errorf("error_detail leaked %q: %q", leak, detail)
		}
	}
	if !strings.Contains(detail, "writing image") {
		t.Errorf("error_detail dropped structural text: %q", detail)
	}
}

func TestTrackCommand_NoErrorDetailForClassifiedBuckets(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	root := newTestRoot()
	executed, _, err := root.Find([]string{"failing"})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	// A gRPC Unavailable classifies as grpc_unavailable (not a catch-all), so
	// no error_detail is attached even though the message has a hostname.
	trackCommand(executed, status.Error(codes.Unavailable, "reach secret-host.example.com"), time.Millisecond)

	got := (*events)[0].props
	if got["error_class"] != "grpc_unavailable" {
		t.Fatalf("error_class = %q, want grpc_unavailable", got["error_class"])
	}
	if _, present := got["error_detail"]; present {
		t.Errorf("error_detail must be absent for classified buckets, got %q", got["error_detail"])
	}
}

func TestTrackCommand_NoErrorDetailOnSuccess(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	executed, err := runWithArgs(t, []string{"wendy", "run", "x"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	trackCommand(executed, nil, time.Millisecond)
	if _, present := (*events)[0].props["error_detail"]; present {
		t.Errorf("error_detail must be absent on success")
	}
}

func TestFormatError_EnrollmentTokenUnavailableIsCloudError(t *testing.T) {
	err := fmt.Errorf("creating enrollment token: %w", status.Error(codes.Unavailable, "connection refused"))

	got := formatError(err).Error()
	want := "creating enrollment token: Could not connect to Wendy Cloud. Please try again later."
	if got != want {
		t.Fatalf("formatError() = %q, want %q", got, want)
	}
	if strings.Contains(got, "device") {
		t.Fatalf("formatError() should not describe enrollment token creation as a device connection: %q", got)
	}
}

func TestFormatError_LocalPKICoreUnavailable(t *testing.T) {
	err := fmt.Errorf("creating enrollment token from pki-core services.orb.local:50051: %w", status.Error(codes.Unavailable, "connection refused"))

	got := formatError(err).Error()
	want := "creating enrollment token from pki-core services.orb.local:50051: Could not connect to local pki-core. Check that the gRPC endpoint is reachable from this machine."
	if got != want {
		t.Fatalf("formatError() = %q, want %q", got, want)
	}
	if strings.Contains(got, "Wendy Cloud") || strings.Contains(got, "device") {
		t.Fatalf("formatError() should describe local pki-core, got %q", got)
	}
}

func TestFormatError_UnimplementedPreservesContextualDescription(t *testing.T) {
	err := fmt.Errorf("listing audio devices: %w", status.Error(codes.Unimplemented, "Audio device management is currently not supported by Wendy Agent for Mac."))

	got := formatError(err).Error()
	want := "listing audio devices: Audio device management is currently not supported by Wendy Agent for Mac."
	if got != want {
		t.Fatalf("formatError() = %q, want %q", got, want)
	}
	if strings.Contains(got, "Try updating") || strings.Contains(got, "rpc error") {
		t.Fatalf("formatError() should not render generic update advice or raw gRPC text: %q", got)
	}
}

func TestFormatError_UnimplementedKeepsUpdateHintForUnknownMethod(t *testing.T) {
	err := fmt.Errorf("listing volumes: %w", status.Error(codes.Unimplemented, "unknown method ListVolumes for service agent.ContainerService"))

	got := formatError(err).Error()
	want := "listing volumes: Not supported by this agent version. Try updating the agent."
	if got != want {
		t.Fatalf("formatError() = %q, want %q", got, want)
	}
}

func TestFormatError_UnimplementedKeepsUpdateHintForGeneratedStub(t *testing.T) {
	err := fmt.Errorf("listing volumes: %w", status.Error(codes.Unimplemented, "method ListVolumes not implemented"))

	got := formatError(err).Error()
	want := "listing volumes: Not supported by this agent version. Try updating the agent."
	if got != want {
		t.Fatalf("formatError() = %q, want %q", got, want)
	}
}

func TestEnv_IsCITripsKillSwitch(t *testing.T) {
	// Sanity check that env.IsCI() recognizes the CI variable. The deeper
	// contract — that analytics.Init refuses to enable in CI — is covered
	// in internal/cli/analytics/analytics_test.go where the config package
	// is already wired up.
	clearCIEnv(t)
	if env.IsCI() {
		t.Fatal("test setup: clearCIEnv should leave IsCI false")
	}
	t.Setenv("CI", "1")
	if !env.IsCI() {
		t.Error("env.IsCI() should be true when CI=1")
	}
}

func TestEventNameFor(t *testing.T) {
	cases := []struct {
		path     string
		homebrew bool
		want     string
	}{
		{"wendy completion install", true, "install_completed"},
		{"wendy completion install", false, "command_executed"},
		{"wendy run", true, "command_executed"},
		{"wendy device info", false, "command_executed"},
	}
	for _, c := range cases {
		if got := eventNameFor(c.path, c.homebrew); got != c.want {
			t.Errorf("eventNameFor(%q, %v) = %q, want %q", c.path, c.homebrew, got, c.want)
		}
	}
}

func TestMilestoneFor(t *testing.T) {
	cases := []struct {
		path    string
		success bool
		want    string
	}{
		{"wendy discover", true, "discover_success"},
		{"wendy init", true, "init_success"},
		{"wendy run", true, "first_deploy_success"},
		{"wendy cloud login", true, "auth_success"},
		{"wendy auth login", true, "auth_success"},
		{"wendy run", false, ""},
		{"wendy device info", true, ""},
	}
	for _, c := range cases {
		if got := milestoneFor(c.path, c.success); got != c.want {
			t.Errorf("milestoneFor(%q, %v) = %q, want %q", c.path, c.success, got, c.want)
		}
	}
}

func TestIsSetupCommand(t *testing.T) {
	setup := []string{"wendy completion install", "wendy __complete", "wendy help", "wendy analytics disable"}
	real := []string{"wendy run", "wendy discover", "wendy device info"}
	for _, p := range setup {
		if !isSetupCommand(p) {
			t.Errorf("isSetupCommand(%q) = false, want true", p)
		}
	}
	for _, p := range real {
		if isSetupCommand(p) {
			t.Errorf("isSetupCommand(%q) = true, want false", p)
		}
	}
}
