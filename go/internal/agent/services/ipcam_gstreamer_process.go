package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

const ipCameraGStreamerUtility = "ipcam-gstreamer"

// newIPCameraGStreamerCommand builds the credential-safe helper invocation.
// The pipeline itself is sent over stdin after Start, so neither the RTSP URL
// nor its password can appear in argv or the environment.
func newIPCameraGStreamerCommand(ctx context.Context, executable string) *exec.Cmd {
	return exec.CommandContext(ctx, executable, "utils", ipCameraGStreamerUtility)
}

// gstreamerFrames runs the in-process GStreamer API inside a short-lived copy of
// the agent executable. This keeps plugins isolated from the server process while
// moving the credential-bearing pipeline over a private stdin pipe instead of a
// process argument. Raw helper stderr is never returned or logged because a
// GStreamer diagnostic may repeat element property values.
func (s *VideoService) gstreamerFrames(ctx context.Context, args []string, onFrame func([]byte)) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating GStreamer helper: %w", err)
	}
	cmd := newIPCameraGStreamerCommand(ctx, executable)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("creating GStreamer input pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating GStreamer output pipe: %w", err)
	}
	// Capture a bounded amount solely to keep the child from blocking if a
	// library writes diagnostics. It is intentionally discarded on every path.
	cmd.Stderr = &limitedBuffer{limit: maxStderrBytes}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting GStreamer helper: %w", err)
	}

	encodeErr := json.NewEncoder(stdin).Encode(args)
	closeErr := stdin.Close()
	if encodeErr != nil {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		return errors.New("sending GStreamer pipeline failed")
	}
	if closeErr != nil {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		return errors.New("closing GStreamer pipeline input failed")
	}

	buf := make([]byte, 64*1024)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			onFrame(chunk)
		}
		if readErr != nil {
			if readErr != io.EOF && ctx.Err() == nil {
				cmd.Process.Kill() //nolint:errcheck
				cmd.Wait()         //nolint:errcheck
				return errors.New("reading GStreamer output failed")
			}
			break
		}
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return errors.New("GStreamer capture pipeline failed")
	}
	return nil
}
