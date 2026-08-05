package commands

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

// clearCIEnv unsets every CI-detection env var (not just "CI") so tests run
// correctly under real CI systems, which set vars like GITHUB_ACTIONS that
// env.IsCI() also checks.
func clearCIEnv(t *testing.T) {
	t.Helper()
	for _, key := range env.CIEnvVars {
		t.Setenv(key, "")
	}
}

func TestMaybeRunCrashReportSkipsRecoverable(t *testing.T) {
	clearCIEnv(t)
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

	clearCIEnv(t)
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

	clearCIEnv(t)
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

func TestReportCrashLocallyOpensBrowserWhenGHPresent(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	var openedURL string
	openBrowser = func(u string) error { openedURL = u; return nil }

	cmd := &cobra.Command{Use: "wendy"}
	var out bytes.Buffer
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "boom"}

	reportCrashLocally(cmd, &out, info, bundle, "/tmp/report.json")

	if !strings.Contains(out.String(), "/tmp/report.json") {
		t.Errorf("out = %q, want it to mention the local file", out.String())
	}
	if openedURL == "" || !strings.Contains(openedURL, "issues/new?") {
		t.Errorf("openBrowser called with %q, want a bug_report.yml issue URL", openedURL)
	}
}

func TestReportCrashLocallyPrintsURLWhenOpenBrowserFails(t *testing.T) {
	origLookPath, origOpenBrowser := lookPath, openBrowser
	t.Cleanup(func() { lookPath = origLookPath; openBrowser = origOpenBrowser })
	lookPath = func(string) (string, error) { return "/usr/local/bin/gh", nil }
	openBrowser = func(string) error { return fmt.Errorf("no display") }

	cmd := &cobra.Command{Use: "wendy"}
	var out bytes.Buffer
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "boom"}

	reportCrashLocally(cmd, &out, info, bundle, "/tmp/report.json")

	if !strings.Contains(out.String(), "Could not open the browser automatically") {
		t.Errorf("out = %q, want the open-failed fallback message", out.String())
	}
	if !strings.Contains(out.String(), "issues/new?") {
		t.Errorf("out = %q, want the report URL", out.String())
	}
}

func TestReportCrashLocallyPrintsURLWhenGHMissing(t *testing.T) {
	origLookPath := lookPath
	t.Cleanup(func() { lookPath = origLookPath })
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	cmd := &cobra.Command{Use: "wendy"}
	var out bytes.Buffer
	info := platforminfo.Info{CLIVersion: "1.2.3"}
	bundle := crashreport.Bundle{ErrorClass: "docker_build_failed", ErrorChain: "boom"}

	reportCrashLocally(cmd, &out, info, bundle, "/tmp/report.json")

	if !strings.Contains(out.String(), "Open a bug report:") {
		t.Errorf("out = %q, want the manual fallback line", out.String())
	}
	if !strings.Contains(out.String(), "issues/new?") {
		t.Errorf("out = %q, want the report URL", out.String())
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
