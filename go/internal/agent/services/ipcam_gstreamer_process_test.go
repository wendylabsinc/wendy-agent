package services

import (
	"context"
	"strings"
	"testing"
)

func TestIPCameraGStreamerCommandKeepsPipelineOutOfArgv(t *testing.T) {
	secret := "camera-password-must-not-be-in-argv"
	cmd := newIPCameraGStreamerCommand(context.Background(), "/agent/wendy-agent")
	if got := strings.Join(cmd.Args, " "); strings.Contains(got, secret) {
		t.Fatalf("helper argv leaked camera password: %q", got)
	}
	if got := strings.Join(cmd.Args, " "); got != "/agent/wendy-agent utils ipcam-gstreamer" {
		t.Fatalf("helper argv = %q", got)
	}
}
