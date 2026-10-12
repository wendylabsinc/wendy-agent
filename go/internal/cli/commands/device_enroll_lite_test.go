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
