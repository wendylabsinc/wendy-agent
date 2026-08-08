//go:build darwin || linux

package services

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
)

func TestGStreamerPipelineDescriptionUsesPrivateFD(t *testing.T) {
	secret := "do-not-put-me-in-argv"
	args := ipcam.PipelineArgs("rtsp://admin:" + secret + "@10.98.0.50/x")
	description, err := gstreamerPipelineDescription(args, 42)
	if err != nil {
		t.Fatalf("gstreamerPipelineDescription: %v", err)
	}
	if strings.Contains(description, "fd=1") || !strings.Contains(description, "fd=42") {
		t.Fatalf("description did not redirect fdsink: %q", description)
	}
}
