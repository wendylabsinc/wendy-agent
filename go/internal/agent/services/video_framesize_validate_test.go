package services

import (
	"testing"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// req is a small helper so the table below reads as sizes, not proto literals.
func req(w, h, fps uint32) *agentpb.StreamVideoRequest {
	return &agentpb.StreamVideoRequest{Width: w, Height: h, Framerate: fps}
}

// TestValidateStreamParamsWithoutDevice covers the fallback path: no device to
// enumerate (empty path, as when the source is not a V4L2 node), so the
// historical allowlist and the absolute bounds are all we have.
func TestValidateStreamParamsWithoutDevice(t *testing.T) {
	tests := []struct {
		name    string
		req     *agentpb.StreamVideoRequest
		wantErr bool
	}{
		{"zero means device default", req(0, 0, 0), false},
		{"common mode still accepted", req(1280, 720, 30), false},
		{"4K still accepted", req(3840, 2160, 0), false},
		{"unknown mode rejected without a device to ask", req(512, 484, 0), true},
		{"width without height", req(640, 0, 0), true},
		{"height without width", req(0, 480, 0), true},
		{"below the lower bound", req(4, 12305, 0), true},
		{"above the upper bound", req(16384, 16384, 0), true},
		{"bad framerate", req(0, 0, 7), true},
		{"good framerate", req(0, 0, 30), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStreamParams("", tc.req)
			if tc.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if err != nil && status.Code(err) != codes.InvalidArgument {
				t.Fatalf("want InvalidArgument, got %v", status.Code(err))
			}
		})
	}
}

// TestValidateStreamParamsBoundsBeforeDevice pins the ordering: the absolute
// bounds are the security property, so they must be enforced before we consult
// the device. A camera advertising a malformed descriptor (the TOPDON TC001
// reports 4x12305 and 60x3299 alongside its real modes) must not be able to
// talk us into an out-of-range pipeline argument.
func TestValidateStreamParamsBoundsBeforeDevice(t *testing.T) {
	// /dev/null is openable but enumerates nothing, standing in for "a device
	// we can open but cannot ask". The bounds must still reject these.
	for _, r := range []*agentpb.StreamVideoRequest{
		req(4, 12305, 0),
		req(60, 3299, 0),
		req(8, 12578, 0),
	} {
		if err := validateStreamParams("/dev/null", r); err == nil {
			t.Fatalf("expected %dx%d to be rejected by the bounds",
				r.GetWidth(), r.GetHeight())
		}
	}
}

// TestDeviceAdvertisesFrameSizeUnopenable documents the contract callers rely
// on: when the device cannot be opened we report "not known", so
// validateStreamParams falls back to the allowlist rather than rejecting
// everything.
func TestDeviceAdvertisesFrameSizeUnopenable(t *testing.T) {
	advertised, known := deviceAdvertisesFrameSize("/definitely/not/a/device", 1280, 720)
	if advertised {
		t.Fatal("a nonexistent device cannot advertise anything")
	}
	if known {
		t.Fatal("a nonexistent device must report its modes as unknown")
	}
}
