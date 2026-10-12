package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
	litepb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"google.golang.org/protobuf/proto"
)

type alreadyEnrolledLiteDevice struct {
	liteEnrollmentDevice // Any provisioning operation would panic in this test.
	closed               bool
}

func (*alreadyEnrolledLiteDevice) GetDeviceIdentity(time.Duration) (*liteclient.DeviceIdentity, error) {
	return &liteclient.DeviceIdentity{ID: "441bf6804824", Name: "joannis-home"}, nil
}

func (*alreadyEnrolledLiteDevice) EnrollmentChallenge(bool) (*litepb.WendyComEnrollmentChallenge, error) {
	return &litepb.WendyComEnrollmentChallenge{Enrolled: true}, nil
}

func (d *alreadyEnrolledLiteDevice) Close() error { d.closed = true; return nil }

func TestLiteEnrollmentReportsRemainingIdentityBeforeNameOrTrustSetup(t *testing.T) {
	old := connectLiteEnrollmentFn
	t.Cleanup(func() { connectLiteEnrollmentFn = old })
	device := &alreadyEnrolledLiteDevice{}
	connectLiteEnrollmentFn = func(models.ExternalDevice) (liteEnrollmentDevice, error) { return device, nil }
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	// Invalid name, auth and trust options must not hide the existing identity
	// or trigger another name prompt after a cloud-only unenrollment.
	err := runEnrollLiteDevice(cmd, liteUnenrollUSBTarget(), &config.AuthConfig{}, "\x1b[A", liteEnrollmentOptions{deviceRoots: "/missing.pem"})
	if err == nil || !strings.Contains(err.Error(), "still holds an issued identity") || !strings.Contains(err.Error(), "wendy device unenroll") || !strings.Contains(err.Error(), "USB") {
		t.Fatalf("missing concrete identity recovery instructions: %v", err)
	}
	if !device.closed {
		t.Fatal("enrollment connection leaked")
	}
}

type unsupportedLiteEnrollmentDevice struct {
	alreadyEnrolledLiteDevice
	supported *bool
}

func (d *unsupportedLiteEnrollmentDevice) EnrollmentChallenge(statusOnly bool) (*litepb.WendyComEnrollmentChallenge, error) {
	if !statusOnly {
		panic("unsupported firmware must not start an enrollment challenge")
	}
	return &litepb.WendyComEnrollmentChallenge{EnrollmentResetSupported: true, CloudEnrollmentSupported: d.supported}, nil
}

func TestLiteEnrollmentRejectsUnsupportedFirmwareBeforeSetup(t *testing.T) {
	old := connectLiteEnrollmentFn
	t.Cleanup(func() { connectLiteEnrollmentFn = old })
	device := &unsupportedLiteEnrollmentDevice{supported: proto.Bool(false)}
	connectLiteEnrollmentFn = func(models.ExternalDevice) (liteEnrollmentDevice, error) { return device, nil }
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	// Reset support alone does not mean the firmware can request a certificate.
	// Reject it before prompting for a name, accessing trust or reserving an asset.
	err := runEnrollLiteDevice(cmd, liteUnenrollUSBTarget(), &config.AuthConfig{}, "\x1b[A", liteEnrollmentOptions{deviceRoots: "/missing.pem"})
	if err == nil || !strings.Contains(err.Error(), "was built without cloud enrollment support") {
		t.Fatalf("expected firmware capability error before enrollment setup, got %v", err)
	}
	if !device.closed {
		t.Fatal("enrollment connection leaked")
	}
}

func TestLiteEnrollmentAllowsLegacyAndSupportedFirmware(t *testing.T) {
	old := connectLiteEnrollmentFn
	t.Cleanup(func() { connectLiteEnrollmentFn = old })
	for _, tc := range []struct {
		name      string
		supported *bool
	}{
		{name: "legacy"},
		{name: "supported", supported: proto.Bool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := &unsupportedLiteEnrollmentDevice{supported: tc.supported}
			connectLiteEnrollmentFn = func(models.ExternalDevice) (liteEnrollmentDevice, error) { return device, nil }
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			// Reaching name validation proves older firmware can still enroll;
			// the invalid name keeps this test from contacting Cloud.
			err := runEnrollLiteDevice(cmd, liteUnenrollUSBTarget(), &config.AuthConfig{}, "\x1b[A", liteEnrollmentOptions{})
			if err == nil || !strings.Contains(err.Error(), "name may only contain") {
				t.Fatalf("firmware was rejected before name validation: %v", err)
			}
			if !device.closed {
				t.Fatal("enrollment connection leaked")
			}
		})
	}
}
