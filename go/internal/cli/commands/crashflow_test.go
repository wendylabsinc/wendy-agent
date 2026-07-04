package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestMaybeRunCrashReportSkipsRecoverable(t *testing.T) {
	t.Setenv("CI", "") // ensure not classified as CI
	t.Setenv("WENDY_CRASHREPORT", "true")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		fmt.Errorf("plain recoverable error"), "other")
	// No panic, returns cleanly.
}

// TestMaybeRunCrashReportSuppressed proves that cfg.CrashReport.Suppressed is
// what stops the flow, by forcing every earlier gate (analytics enabled,
// interactive terminal, CI, env opt-in) open first.
func TestMaybeRunCrashReportSuppressed(t *testing.T) {
	origAnalytics := analyticsEnabledFn
	origInteractive := isInteractiveTerminalFn
	t.Cleanup(func() {
		analyticsEnabledFn = origAnalytics
		isInteractiveTerminalFn = origInteractive
	})
	analyticsEnabledFn = func() bool { return true }
	isInteractiveTerminalFn = func() bool { return true }

	t.Setenv("CI", "")
	t.Setenv("WENDY_CRASHREPORT", "true")
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{Suppressed: true}})

	cmd := &cobra.Command{Use: "wendy"}
	var buf bytes.Buffer
	cmd.SetErr(&buf)

	// Even an unrecoverable build failure must be a no-op when suppressed.
	MaybeRunCrashReport(context.Background(), cmd,
		diag.MarkBuildFailure(fmt.Errorf("docker build failed")), "other")

	if buf.Len() != 0 {
		t.Errorf("expected no output when suppressed, got %q", buf.String())
	}
}

// TestMaybeRunCrashReportNotSuppressedProceeds is the paired control case: with
// the same gates forced open but Suppressed=false, the flow must proceed past
// the suppression check (it will then hit EOF reading the consent prompt from
// stdin and skip, but only after printing the unrecoverable-failure banner).
func TestMaybeRunCrashReportNotSuppressedProceeds(t *testing.T) {
	origAnalytics := analyticsEnabledFn
	origInteractive := isInteractiveTerminalFn
	t.Cleanup(func() {
		analyticsEnabledFn = origAnalytics
		isInteractiveTerminalFn = origInteractive
	})
	analyticsEnabledFn = func() bool { return true }
	isInteractiveTerminalFn = func() bool { return true }

	t.Setenv("CI", "")
	t.Setenv("WENDY_CRASHREPORT", "true")
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{Suppressed: false}})

	cmd := &cobra.Command{Use: "wendy"}
	var buf bytes.Buffer
	cmd.SetErr(&buf)

	MaybeRunCrashReport(context.Background(), cmd,
		diag.MarkBuildFailure(fmt.Errorf("docker build failed")), "other")

	if !strings.Contains(buf.String(), "unrecoverable failure") {
		t.Errorf("expected output to contain %q, got %q", "unrecoverable failure", buf.String())
	}
}

func TestCrashConsentPrompt(t *testing.T) {
	cases := map[string]crashConsent{"y\n": consentSend, "yes\n": consentSend, "n\n": consentSkip, "\n": consentSkip, "d\n": consentSuppress, "don't\n": consentSuppress}
	for in, want := range cases {
		if got := parseCrashConsent(in); got != want {
			t.Errorf("parseCrashConsent(%q) = %v, want %v", in, got, want)
		}
	}
}
