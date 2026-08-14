//go:build linux

package services

import (
	"context"
	"strings"
	"testing"
)

// Linux does not require GStreamer initialization on the process's initial OS
// thread, so the unit test can exercise the library runner directly. The Darwin
// path is covered by invoking the real helper binary in validation.
func TestGStreamerInProcessRunner(t *testing.T) {
	if _, err := loadGStreamer(); err != nil {
		t.Skip(err)
	}
	var output []byte
	err := runIPCameraGStreamerPipeline(context.Background(), []string{
		"fakesrc", "num-buffers=1", "sizetype=fixed", "sizemax=8",
		"!", "fdsink", "fd=1",
	}, func(chunk []byte) error {
		output = append(output, chunk...)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("runner error = %v, want terminal pipeline error", err)
	}
	if len(output) != 8 {
		t.Fatalf("output bytes = %d, want 8", len(output))
	}
}
