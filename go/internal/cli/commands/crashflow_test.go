package commands

import (
	"context"
	"fmt"
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

func TestMaybeRunCrashReportSuppressed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = config.Save(&config.Config{CrashReport: &config.CrashReportConfig{Suppressed: true}})
	// Even an unrecoverable build failure must be a no-op when suppressed.
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		diag.MarkBuildFailure(fmt.Errorf("docker build failed")), "other")
}

func TestCrashConsentPrompt(t *testing.T) {
	cases := map[string]crashConsent{"y\n": consentSend, "yes\n": consentSend, "n\n": consentSkip, "\n": consentSkip, "d\n": consentSuppress, "don't\n": consentSuppress}
	for in, want := range cases {
		if got := parseCrashConsent(in); got != want {
			t.Errorf("parseCrashConsent(%q) = %v, want %v", in, got, want)
		}
	}
}
