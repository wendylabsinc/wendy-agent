package services

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// cameraInUseReason lets clients branch on contention. The code alone cannot: preflightTegraCSI
// returns FailedPrecondition too, for a firmware mismatch.
const cameraInUseReason = "CAMERA_IN_USE"

// Reaches the cloud dashboard verbatim, so it carries no host detail.
const cameraInUseMessage = "camera is already in use by another application on this device; " +
	"stop the app holding it and retry"

// Stderr is the only contention signal on the GStreamer path. Matching stays narrow: an
// unrecognised failure must read as generic rather than be mislabelled as contention.
var busyStderrSignatures = []string{
	"resource busy",               // kernel EBUSY text
	"is busy",                     // v4l2src: Device '/dev/video0' is busy
	"already in use",              // libcamera, Argus
	"failed to acquire camera",    // libcamera
	"cannot create camera device", // nvargus, sensor already claimed
}

func isBusyStderr(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, signature := range busyStderrSignatures {
		if strings.Contains(lower, signature) {
			return true
		}
	}
	return false
}

// Accepts both a unix.Errno from a raw syscall and the error from unix.Open.
func isBusyErrno(err error) bool {
	return errors.Is(err, unix.EBUSY)
}

// Our own producer can still hold the device fd for a moment after a stream ends, so a
// reconnect sees EBUSY. Waiting it out keeps that from being reported as contention, which
// would tell the user to stop an app that does not exist.
const (
	busyRetries    = 4
	busyRetryDelay = 300 * time.Millisecond
)

// retryWhileBusy re-runs op while it reports EBUSY, up to busyRetries times.
func retryWhileBusy(ctx context.Context, op func() unix.Errno) unix.Errno {
	errno := op()
	for attempt := 0; attempt < busyRetries && isBusyErrno(errno); attempt++ {
		select {
		case <-ctx.Done():
			return errno
		case <-time.After(busyRetryDelay):
		}
		errno = op()
	}
	return errno
}

// errCameraInUse reports contention to the client. V4L2 and GStreamer diagnostics stay in the
// agent log; echoing them to an authenticated client would leak host topology.
func errCameraInUse(devicePath string) error {
	st := status.New(codes.FailedPrecondition, cameraInUseMessage)
	withInfo, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   cameraInUseReason,
		Domain:   "wendy.dev",
		Metadata: map[string]string{"device": filepath.Base(devicePath)},
	})
	if err != nil {
		return st.Err()
	}
	return withInfo.Err()
}
