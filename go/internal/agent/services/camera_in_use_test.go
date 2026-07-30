package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func errorInfoOf(t *testing.T, err error) (*status.Status, *errdetails.ErrorInfo) {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a gRPC status: %v", err)
	}
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok {
			return st, info
		}
	}
	return st, nil
}

func TestErrCameraInUseCarriesReason(t *testing.T) {
	st, info := errorInfoOf(t, errCameraInUse("/dev/video2"))

	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", st.Code())
	}
	if !strings.Contains(st.Message(), "already in use") {
		t.Errorf("message %q does not name the cause", st.Message())
	}
	if info == nil {
		t.Fatal("status carries no ErrorInfo detail, so clients cannot branch on it")
	}
	if info.GetReason() != cameraInUseReason {
		t.Errorf("reason = %q, want %q", info.GetReason(), cameraInUseReason)
	}
	if info.GetDomain() != "wendy.dev" {
		t.Errorf("domain = %q, want wendy.dev", info.GetDomain())
	}
	if got := info.GetMetadata()["device"]; got != "video2" {
		t.Errorf("device metadata = %q, want video2", got)
	}
}

// Same constraint that keeps GStreamer stderr out of the gRPC response.
func TestCameraInUseMessageCarriesNoHostDetail(t *testing.T) {
	st, _ := status.FromError(errCameraInUse("/dev/video0"))
	for _, leak := range []string{"/dev/", "nvargus", "gstreamer", "v4l2", "vidioc", "errno"} {
		if strings.Contains(strings.ToLower(st.Message()), leak) {
			t.Errorf("message %q leaks %q", st.Message(), leak)
		}
	}
}

func TestIsBusyStderr(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			// Captured from gst-plugins-good against a held UVC camera. The path is
			// interposed ("Device '/dev/video0' is busy"), and a not-negotiated line rides
			// along in the same stderr - contention must still win.
			name: "v4l2src on a camera held by another process",
			stderr: "ERROR: from element /GstPipeline:pipeline0/GstV4l2Src:v4l2src0: Device '/dev/video0' is busy\n" +
				"Additional debug info:\n" +
				"../sys/v4l2/gstv4l2object.c(4417): gst_v4l2_object_set_format_full ():\n" +
				"Call to S_FMT failed for MJPG @ 1280x720: Device or resource busy\n" +
				"ERROR: from element /GstPipeline:pipeline0/GstV4l2Src:v4l2src0: Internal data stream error.\n" +
				"streaming stopped, reason not-negotiated (-4)",
			want: true,
		},
		{
			name:   "kernel EBUSY text in the debug line",
			stderr: "Could not open device '/dev/video0' for reading and writing.\nsystem error: Device or resource busy",
			want:   true,
		},
		{
			name:   "libcamera cannot acquire",
			stderr: "ERROR libcamera camera.cpp:222 Failed to acquire camera",
			want:   true,
		},
		{
			name:   "argus sensor already claimed",
			stderr: "Error generated: Camera is already in use by another process",
			want:   true,
		},
		{
			name:   "nvargus cannot create the device",
			stderr: "(Argus) Error BadParameter: Cannot create camera device (in src/api/CameraProviderImpl.cpp)",
			want:   true,
		},
		{
			// Mislabelling this would send the user hunting a process that does not exist.
			name:   "unsupported caps is not contention",
			stderr: "ERROR: from element capsfilter0: not negotiated\nstreaming stopped, reason not-negotiated (-4)",
			want:   false,
		},
		{
			name:   "missing element is not contention",
			stderr: "WARNING: erroneous pipeline: no element \"nvarguscamerasrc\"",
			want:   false,
		},
		{
			name:   "empty stderr",
			stderr: "",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBusyStderr(tc.stderr); got != tc.want {
				t.Errorf("isBusyStderr(%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

func TestRetryWhileBusy(t *testing.T) {
	t.Run("succeeds once the device is released", func(t *testing.T) {
		calls := 0
		errno := retryWhileBusy(context.Background(), func() unix.Errno {
			calls++
			if calls < 3 {
				return unix.EBUSY
			}
			return 0
		})

		if errno != 0 {
			t.Errorf("errno = %v, want 0", errno)
		}
		if calls != 3 {
			t.Errorf("calls = %d, want 3", calls)
		}
	})

	t.Run("gives up so real contention is still reported", func(t *testing.T) {
		calls := 0
		errno := retryWhileBusy(context.Background(), func() unix.Errno {
			calls++
			return unix.EBUSY
		})

		if !isBusyErrno(errno) {
			t.Errorf("errno = %v, want EBUSY", errno)
		}
		if calls != busyRetries+1 {
			t.Errorf("calls = %d, want %d", calls, busyRetries+1)
		}
	})

	t.Run("does not retry a non-busy failure", func(t *testing.T) {
		calls := 0
		errno := retryWhileBusy(context.Background(), func() unix.Errno {
			calls++
			return unix.EINVAL
		})

		if errno != unix.EINVAL || calls != 1 {
			t.Errorf("errno = %v after %d calls, want EINVAL after 1", errno, calls)
		}
	})

	t.Run("stops on context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		errno := retryWhileBusy(ctx, func() unix.Errno { return unix.EBUSY })

		if !isBusyErrno(errno) {
			t.Errorf("errno = %v, want EBUSY", errno)
		}
	})
}

func TestIsBusyErrno(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"raw errno from ioctl", unix.EBUSY, true},
		{"wrapped errno from open", fmt.Errorf("open /dev/video0: %w", unix.EBUSY), true},
		{"invalid argument", unix.EINVAL, false},
		{"not found", unix.ENOENT, false},
		{"permission denied", unix.EACCES, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBusyErrno(tc.err); got != tc.want {
				t.Errorf("isBusyErrno(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
