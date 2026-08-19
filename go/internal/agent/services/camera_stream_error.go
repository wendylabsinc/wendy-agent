package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reasonCameraInUse is the ErrorInfo reason for a camera another process holds AND that
// could not be read through the PipeWire graph either -- reaching it means sharing was
// unavailable, not that a camera inherently allows one consumer.
const reasonCameraInUse = "CAMERA_IN_USE"

// errCameraInUse reports contention as a FailedPrecondition: the device is
// healthy, its state is wrong, and the caller can retry once the holder stops.
func errCameraInUse(devicePath string) error {
	st := status.New(codes.FailedPrecondition,
		fmt.Sprintf("camera %s is already in use by another application on this device", devicePath))
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   reasonCameraInUse,
		Metadata: map[string]string{"device": devicePath},
	})
	if err != nil {
		return st.Err()
	}
	return detailed.Err()
}

// isCameraInUse reports whether err is the contention error above.
func isCameraInUse(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetReason() == reasonCameraInUse {
			return true
		}
	}
	return false
}

// isBusyErrno reports whether a syscall failed because the device is claimed.
func isBusyErrno(err error) bool {
	return errors.Is(err, unix.EBUSY)
}

// retryWhileBusy re-runs op briefly while it reports EBUSY: a reconnect can land while the
// agent's own previous producer still holds the device, which is not another application.
func retryWhileBusy(ctx context.Context, op func() unix.Errno) unix.Errno {
	const (
		attempts = 4
		delay    = 150 * time.Millisecond
	)
	errno := op()
	for i := 0; i < attempts && isBusyErrno(errno); i++ {
		select {
		case <-ctx.Done():
			return errno
		case <-time.After(delay):
		}
		errno = op()
	}
	return errno
}

// busyPhrases are how the capture elements word contention. GStreamer reports it
// only as prose on stderr, and each library words it differently.
var busyPhrases = []string{
	"resource busy",               // kernel EBUSY text
	"is busy",                     // v4l2src: Device '/dev/video0' is busy
	"already in use",              // libcamera, Argus
	"failed to acquire camera",    // libcamera
	"cannot create camera device", // nvargus, sensor already claimed
}

// captureElements name the sources that read a camera. An encoder returns EBUSY in the
// same wording, so a busy phrase alone would blame the camera for a contended encoder.
var captureElements = []string{"v4l2src", "libcamerasrc", "nvarguscamerasrc", "pipewiresrc", "argus"}

// isBusyStderr reports whether captured stderr describes camera contention. Per error
// record: gst splits one failure across its "from element" line and the debug block below,
// while a warning it recovered from must not lend its element name to a later failure.
func isBusyStderr(stderr, devicePath string) bool {
	device := strings.ToLower(devicePath)
	for _, record := range errorRecords(strings.ToLower(stderr)) {
		if !containsAny(record, busyPhrases) {
			continue
		}
		if namesDevice(record, device) || containsAny(record, captureElements) {
			return true
		}
	}
	return false
}

// exitedOnError reports whether the process failed by itself rather than being killed
// during teardown. streamGStreamer always signals the pipeline, so a non-nil Wait error
// proves nothing on its own.
func exitedOnError(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return !ws.Signaled() || ws.Signal() != syscall.SIGKILL
}

// errorRecords splits already-lowercased gst stderr at each error or warning marker,
// keeping a message and the debug block that explains it together as one unit.
func errorRecords(stderr string) []string {
	var records []string
	var cur strings.Builder
	for _, line := range strings.Split(stderr, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "error") || strings.HasPrefix(trimmed, "warn") {
			if cur.Len() > 0 {
				records = append(records, cur.String())
				cur.Reset()
			}
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	if cur.Len() > 0 {
		records = append(records, cur.String())
	}
	return records
}

// namesDevice reports whether s mentions exactly devicePath. A plain substring test
// would let /dev/video1 match /dev/video11 — on a Pi that is the H.264 encoder, so a
// contended encoder would be blamed on the camera.
func namesDevice(s, devicePath string) bool {
	if devicePath == "" {
		return false
	}
	for i := 0; i+len(devicePath) <= len(s); {
		j := strings.Index(s[i:], devicePath)
		if j < 0 {
			return false
		}
		end := i + j + len(devicePath)
		if end == len(s) || s[end] < '0' || s[end] > '9' {
			return true
		}
		i = end
	}
	return false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
