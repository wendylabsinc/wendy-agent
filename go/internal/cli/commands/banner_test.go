package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrintPlatformBannerWritesToStderr(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	printPlatformBanner(cmd, false)
	if !strings.Contains(stderr.String(), "wendy ") {
		t.Errorf("banner missing: %q", stderr.String())
	}
}

func TestPrintPlatformBannerSuppressed(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "1")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	printPlatformBanner(cmd, false)
	if stderr.Len() != 0 {
		t.Errorf("banner should be suppressed, got %q", stderr.String())
	}
}
