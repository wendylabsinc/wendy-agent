package streamreason

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewCarriesReasonDomainAndMetadata(t *testing.T) {
	err := New(codes.FailedPrecondition, "camera /dev/video0 is already in use",
		CameraInUse, map[string]string{"device": "/dev/video0"})

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a gRPC status: %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", st.Code())
	}
	info := Info(err)
	if info == nil {
		t.Fatal("no ErrorInfo attached")
	}
	if info.GetReason() != CameraInUse {
		t.Errorf("reason = %q, want %q", info.GetReason(), CameraInUse)
	}
	// Domain drifted between the three hand-rolled constructions this replaced.
	if info.GetDomain() != Domain {
		t.Errorf("domain = %q, want %q", info.GetDomain(), Domain)
	}
	if got := info.GetMetadata()["device"]; got != "/dev/video0" {
		t.Errorf("metadata device = %q, want /dev/video0", got)
	}
}

func TestInfoAndHasIgnoreUnrelatedErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("boom")},
		{"status without ErrorInfo", status.Error(codes.Internal, "failed to start video capture")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if info := Info(c.err); info != nil {
				t.Errorf("Info = %v, want nil", info)
			}
			if Has(c.err, CameraInUse) {
				t.Error("Has = true, want false")
			}
		})
	}
}

func TestHasDistinguishesReasons(t *testing.T) {
	err := New(codes.FailedPrecondition, "no credentials", IPCameraNoCredentials, nil)
	if !Has(err, IPCameraNoCredentials) {
		t.Error("Has(IPCameraNoCredentials) = false, want true")
	}
	// The reasons are a switch key on the CLI side, so a near miss must not match.
	if Has(err, CameraInUse) {
		t.Error("Has(CameraInUse) = true, want false")
	}
}
