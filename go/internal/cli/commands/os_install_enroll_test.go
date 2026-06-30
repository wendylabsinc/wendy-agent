//go:build darwin || linux || windows

package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// stubEnrollPrompts replaces the interactive hooks used by the enrollment
// flow and restores them on cleanup. Each stub fails the test if invoked,
// so individual tests override only the hooks they expect to fire.
func stubEnrollPrompts(t *testing.T) {
	t.Helper()
	origSession := promptEnrollmentSession
	origPreEnroll := confirmPreEnroll
	origContinue := confirmContinueUnenrolled
	origEnroll := preEnrollDeviceFn
	origResolveOrg := resolveOrgFn
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) {
		t.Fatal("unexpected session picker")
		return "", nil
	}
	confirmPreEnroll = func() (bool, error) {
		t.Fatal("unexpected pre-enroll confirm")
		return false, nil
	}
	confirmContinueUnenrolled = func() (bool, error) {
		t.Fatal("unexpected continue-unenrolled confirm")
		return false, nil
	}
	preEnrollDeviceFn = func(context.Context, *config.AuthConfig, string, int32, PreEnrollDialer) (*PreProvisionedState, error) {
		t.Fatal("unexpected enrollment call")
		return nil, nil
	}
	// Default org resolver returns a stable test org so tests that reach
	// preEnrollDeviceFn don't need a live cloud connection.
	resolveOrgFn = func(_ context.Context, _ *config.AuthConfig, _ bool) (OrgResolution, error) {
		return OrgResolution{ID: 7, Name: "Test Org"}, nil
	}
	t.Cleanup(func() {
		promptEnrollmentSession = origSession
		confirmPreEnroll = origPreEnroll
		confirmContinueUnenrolled = origContinue
		preEnrollDeviceFn = origEnroll
		resolveOrgFn = origResolveOrg
	})
}

func twoSessionConfig() *config.Config {
	return &config.Config{Auth: []config.AuthConfig{
		{
			CloudDashboard: "https://cloud.wendy.sh",
			CloudGRPC:      "prod.example.com:443",
			Certificates:   []config.CertificateInfo{{OrganizationID: 7}},
		},
		{
			CloudDashboard: "http://localhost:3000",
			CloudGRPC:      "localhost:50051",
			Certificates:   []config.CertificateInfo{{OrganizationID: 1}},
		},
	}}
}

func TestSelectEnrollmentAuthNoSessions(t *testing.T) {
	stubEnrollPrompts(t)
	_, err := selectEnrollmentAuth(&config.Config{}, "", true)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected not-logged-in error, got %v", err)
	}
}

func TestSelectEnrollmentAuthSingleSession(t *testing.T) {
	stubEnrollPrompts(t)
	cfg := &config.Config{Auth: []config.AuthConfig{{
		CloudGRPC:    "prod.example.com:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 7}},
	}}}
	auth, err := selectEnrollmentAuth(cfg, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil || auth.CloudGRPC != "prod.example.com:443" {
		t.Fatalf("got %+v; want the single session", auth)
	}
}

func TestSelectEnrollmentAuthSingleSessionNoCerts(t *testing.T) {
	stubEnrollPrompts(t)
	cfg := &config.Config{Auth: []config.AuthConfig{{CloudGRPC: "prod.example.com:443"}}}
	_, err := selectEnrollmentAuth(cfg, "", true)
	if err == nil || !strings.Contains(err.Error(), "no certificates") {
		t.Fatalf("expected no-certificates error, got %v", err)
	}
}

func TestSelectEnrollmentAuthCloudGRPCFlag(t *testing.T) {
	stubEnrollPrompts(t)
	auth, err := selectEnrollmentAuth(twoSessionConfig(), "localhost:50051", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("got %+v; want the localhost session", auth)
	}
}

func TestSelectEnrollmentAuthCloudGRPCFlagNoMatch(t *testing.T) {
	stubEnrollPrompts(t)
	_, err := selectEnrollmentAuth(twoSessionConfig(), "missing.example.com:443", true)
	if err == nil || !strings.Contains(err.Error(), "no auth session for missing.example.com:443") {
		t.Fatalf("expected no-session error, got %v", err)
	}
}

func TestSelectEnrollmentAuthMultipleNonInteractive(t *testing.T) {
	stubEnrollPrompts(t)
	_, err := selectEnrollmentAuth(twoSessionConfig(), "", false)
	if err == nil || !strings.Contains(err.Error(), "--cloud-grpc") {
		t.Fatalf("expected multi-session error mentioning --cloud-grpc, got %v", err)
	}
}

func TestSelectEnrollmentAuthMultipleInteractivePicker(t *testing.T) {
	stubEnrollPrompts(t)
	var gotItems []tui.PickerItem
	promptEnrollmentSession = func(items []tui.PickerItem) (string, error) {
		gotItems = items
		return "1", nil // second session
	}

	auth, err := selectEnrollmentAuth(twoSessionConfig(), "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("got %+v; want the localhost session", auth)
	}
	if len(gotItems) != 3 {
		t.Fatalf("picker should list 2 sessions + skip, got %d items", len(gotItems))
	}
	last := gotItems[len(gotItems)-1]
	if last.Value != skipEnrollmentValue {
		t.Fatalf("last picker item should be the skip option, got %+v", last)
	}
	if !strings.Contains(gotItems[0].Description, "org 7") {
		t.Errorf("session items should show the org id, got %q", gotItems[0].Description)
	}
}

func TestSelectEnrollmentAuthSkipOption(t *testing.T) {
	stubEnrollPrompts(t)
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) {
		return skipEnrollmentValue, nil
	}
	auth, err := selectEnrollmentAuth(twoSessionConfig(), "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth != nil {
		t.Fatalf("skip must return nil auth, got %+v", auth)
	}
}

func TestSelectEnrollmentAuthPickerCancelled(t *testing.T) {
	stubEnrollPrompts(t)
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) {
		return "", ErrUserCancelled
	}
	_, err := selectEnrollmentAuth(twoSessionConfig(), "", true)
	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("expected ErrUserCancelled, got %v", err)
	}
}

func TestResolvePreEnrollmentSkipMode(t *testing.T) {
	stubEnrollPrompts(t)
	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollSkip}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("skip mode must be a no-op, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentAutoNonInteractive(t *testing.T) {
	stubEnrollPrompts(t)
	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, false, "dev")
	if err != nil || prov != nil {
		t.Fatalf("auto mode without a TTY must be a no-op, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentAutoNoSessions(t *testing.T) {
	stubEnrollPrompts(t)
	prov, err := resolvePreEnrollment(context.Background(), &config.Config{}, preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("auto mode without sessions must be a no-op, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentAutoDeclined(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return false, nil }
	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("declining the pre-enroll offer must be a no-op, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentSuccess(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return true, nil }
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) { return "0", nil }
	preEnrollDeviceFn = func(_ context.Context, auth *config.AuthConfig, name string, _ int32, _ PreEnrollDialer) (*PreProvisionedState, error) {
		if auth.CloudGRPC != "prod.example.com:443" {
			t.Fatalf("enrolled against %s; want the picked session", auth.CloudGRPC)
		}
		if name != "dev" {
			t.Fatalf("device name %q; want dev", name)
		}
		return &PreProvisionedState{Enrolled: true}, nil
	}

	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if prov == nil || !prov.Enrolled {
		t.Fatalf("got %+v; want enrolled provisioning state", prov)
	}
}

func TestResolvePreEnrollmentUserSkips(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return true, nil }
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) { return skipEnrollmentValue, nil }

	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("explicit skip must continue without JSON, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentFailureAcknowledged(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return true, nil }
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) { return "0", nil }
	preEnrollDeviceFn = func(context.Context, *config.AuthConfig, string, int32, PreEnrollDialer) (*PreProvisionedState, error) {
		return nil, errors.New("cloud unreachable")
	}
	confirmContinueUnenrolled = func() (bool, error) { return true, nil }

	prov, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("acknowledged failure must continue without JSON, got %v / %v", prov, err)
	}
}

func TestResolvePreEnrollmentFailureDeclinedCancelsInstall(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return true, nil }
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) { return "0", nil }
	preEnrollDeviceFn = func(context.Context, *config.AuthConfig, string, int32, PreEnrollDialer) (*PreProvisionedState, error) {
		return nil, errors.New("cloud unreachable")
	}
	confirmContinueUnenrolled = func() (bool, error) { return false, nil }

	_, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("declining must cancel the install, got %v", err)
	}
}

func TestResolvePreEnrollmentForcedNonInteractiveFailureIsFatal(t *testing.T) {
	stubEnrollPrompts(t)
	cfg := &config.Config{Auth: []config.AuthConfig{{
		CloudGRPC:    "prod.example.com:443",
		Certificates: []config.CertificateInfo{{OrganizationID: 7}},
	}}}
	preEnrollDeviceFn = func(context.Context, *config.AuthConfig, string, int32, PreEnrollDialer) (*PreProvisionedState, error) {
		return nil, errors.New("cloud unreachable")
	}

	_, err := resolvePreEnrollment(context.Background(), cfg, preEnrollOptions{mode: preEnrollForced}, false, "dev")
	if err == nil || !strings.Contains(err.Error(), "--pre-enroll") {
		t.Fatalf("non-interactive --pre-enroll failure must be fatal, got %v", err)
	}
}

func TestResolvePreEnrollmentForcedNonInteractiveMultiSession(t *testing.T) {
	stubEnrollPrompts(t)
	_, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollForced}, false, "dev")
	if err == nil || !strings.Contains(err.Error(), "--cloud-grpc") {
		t.Fatalf("expected fatal multi-session error mentioning --cloud-grpc, got %v", err)
	}
}

func TestResolvePreEnrollmentForcedSkipsConfirm(t *testing.T) {
	stubEnrollPrompts(t)
	// confirmPreEnroll stays at the t.Fatal stub: forced mode must never ask
	// "Pre-enroll this device?".
	promptEnrollmentSession = func([]tui.PickerItem) (string, error) { return "0", nil }
	preEnrollDeviceFn = func(context.Context, *config.AuthConfig, string, int32, PreEnrollDialer) (*PreProvisionedState, error) {
		return &PreProvisionedState{}, nil
	}
	if _, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollForced}, true, "dev"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectEnrollmentAuthUsesDefault(t *testing.T) {
	stubEnrollPrompts(t) // picker stub fails the test if invoked
	cfg := twoSessionConfig()
	cfg.DefaultCloudGRPC = "localhost:50051"
	auth, err := selectEnrollmentAuth(cfg, "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if auth == nil || auth.CloudGRPC != "localhost:50051" {
		t.Fatalf("default should be used without the picker, got %+v", auth)
	}
}

func TestMapConfirmCancel(t *testing.T) {
	if _, err := mapConfirmCancel(false, tui.ErrCancelled); !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("tui.ErrCancelled must map to ErrUserCancelled, got %v", err)
	}
	if ok, err := mapConfirmCancel(true, nil); !ok || err != nil {
		t.Fatalf("clean result must pass through, got %v / %v", ok, err)
	}
	otherErr := errors.New("boom")
	if _, err := mapConfirmCancel(false, otherErr); !errors.Is(err, otherErr) {
		t.Fatalf("other errors must pass through, got %v", err)
	}
}

func TestResolvePreEnrollmentConfirmCancelled(t *testing.T) {
	stubEnrollPrompts(t)
	confirmPreEnroll = func() (bool, error) { return false, ErrUserCancelled }

	_, err := resolvePreEnrollment(context.Background(), twoSessionConfig(), preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if !errors.Is(err, ErrUserCancelled) {
		t.Fatalf("cancel at the pre-enroll confirm must cancel the install, got %v", err)
	}
}

func TestResolvePreEnrollmentAuthErrorInteractiveAsksToContinue(t *testing.T) {
	stubEnrollPrompts(t)
	// Single session without certificates: selection fails, user accepts
	// continuing unenrolled.
	cfg := &config.Config{Auth: []config.AuthConfig{{CloudGRPC: "prod.example.com:443"}}}
	confirmPreEnroll = func() (bool, error) { return true, nil }
	confirmContinueUnenrolled = func() (bool, error) { return true, nil }

	prov, err := resolvePreEnrollment(context.Background(), cfg, preEnrollOptions{mode: preEnrollAuto}, true, "dev")
	if err != nil || prov != nil {
		t.Fatalf("acknowledged auth failure must continue without JSON, got %v / %v", prov, err)
	}
}
