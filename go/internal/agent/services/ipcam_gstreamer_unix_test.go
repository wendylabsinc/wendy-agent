//go:build darwin || linux

package services

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/ipcam"
)

func TestGStreamerPipelineDescription_FDTokenRewritten(t *testing.T) {
	secret := "do-not-put-me-in-argv"
	args := ipcam.PipelineArgs("rtsp://admin:" + secret + "@10.98.0.50/x")
	description, hasFD, err := gstreamerPipelineDescription(args, 42)
	if err != nil {
		t.Fatalf("gstreamerPipelineDescription: %v", err)
	}
	if !hasFD {
		t.Fatal("hasFD = false, want true for a pipeline with an fd=1 token")
	}
	if strings.Contains(description, "fd=1") || !strings.Contains(description, "fd=42") {
		t.Fatalf("description did not redirect fdsink: %q", description)
	}
}

// LoopbackPipelineArgs (Task C3's v4l2loopback pump) carries no fd=1 token at
// all — the device node is the sink — so its absence must build a valid
// sink-terminated pipeline description instead of erroring.
func TestGStreamerPipelineDescription_NoFDTokenIsSinkPipeline(t *testing.T) {
	args := ipcam.LoopbackPipelineArgs("rtsp://admin:hunter2@10.98.0.50/x", "/dev/video203")
	description, hasFD, err := gstreamerPipelineDescription(args, 42)
	if err != nil {
		t.Fatalf("gstreamerPipelineDescription: %v", err)
	}
	if hasFD {
		t.Fatal("hasFD = true, want false for a sink-terminated pipeline")
	}
	if !strings.Contains(description, "v4l2sink") || !strings.Contains(description, "/dev/video203") {
		t.Fatalf("description = %q, want the v4l2sink pipeline unchanged", description)
	}
	if strings.Contains(description, "fd=42") {
		t.Fatalf("description = %q, rewrote a token that was never there", description)
	}
}
