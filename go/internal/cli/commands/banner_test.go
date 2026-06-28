package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

func TestPrintTargetBanner(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	resp := &agentpb.GetAgentVersionResponse{
		Version: "0.9.1", Os: "wendyos",
	}
	dt := "jetson-orin-nano"
	resp.DeviceType = &dt
	printTargetBanner(cmd, resp)
	if !strings.Contains(stderr.String(), "jetson-orin-nano") || !strings.Contains(stderr.String(), "0.9.1") {
		t.Errorf("target banner missing fields: %q", stderr.String())
	}
}

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
