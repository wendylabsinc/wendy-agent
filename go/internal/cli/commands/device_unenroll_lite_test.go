package commands

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/liteclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

type fakeLiteUnenrollmentDevice struct {
	identity *liteclient.DeviceIdentity
	err      error
	timeout  time.Duration
	closed   bool
}

func (d *fakeLiteUnenrollmentDevice) GetDeviceIdentity(timeout time.Duration) (*liteclient.DeviceIdentity, error) {
	d.timeout = timeout
	return d.identity, d.err
}

func (d *fakeLiteUnenrollmentDevice) Close() error {
	d.closed = true
	return nil
}

func preserveLiteUnenrollmentHooks(t *testing.T) {
	t.Helper()
	connect, unenroll := connectLiteUnenrollmentFn, unenrollLiteCloudFn
	t.Cleanup(func() {
		connectLiteUnenrollmentFn, unenrollLiteCloudFn = connect, unenroll
	})
}

func TestLiteUnenrollUsesIdentityWithoutEnrollmentStatus(t *testing.T) {
	preserveLiteUnenrollmentHooks(t)
	for _, transport := range []string{"USB", "LAN", "BLE"} {
		t.Run(transport, func(t *testing.T) {
			target := models.ExternalDevice{
				ID:             "wendy-lite:discovery-id",
				DisplayName:    "new-name",
				ProviderKey:    "wendy-lite",
				ConnectionInfo: map[string]string{"type": transport},
			}
			device := &fakeLiteUnenrollmentDevice{identity: &liteclient.DeviceIdentity{ID: "441bf6804824", Name: "old-name"}}
			connectLiteUnenrollmentFn = func(got models.ExternalDevice) (liteUnenrollmentDevice, error) {
				if !reflect.DeepEqual(got, target) {
					t.Fatalf("connected to %v, want %v", got, target)
				}
				return device, nil
			}
			cmd := &cobra.Command{}
			called := false
			wantErr := errors.New("cloud unenrollment result")
			unenrollLiteCloudFn = func(gotCmd *cobra.Command, cloudGRPC, selector, deviceID string, assumeYes bool) error {
				called = true
				if !device.closed {
					t.Fatal("device connection remained open during cloud removal")
				}
				if gotCmd != cmd || cloudGRPC != "cloud.example:443" || selector != "" || deviceID != "lite-441bf6804824" || !assumeYes {
					t.Fatalf("wrong cloud unenrollment arguments: %p %q %q %q %v", gotCmd, cloudGRPC, selector, deviceID, assumeYes)
				}
				return wantErr
			}
			err := runLiteDeviceUnenroll(cmd, target, "cloud.example:443", true)
			if !called || !errors.Is(err, wantErr) {
				t.Fatalf("cloud result lost: called=%v err=%v", called, err)
			}
			if device.timeout != 5*time.Second {
				t.Fatalf("unbounded or changed identity timeout: %v", device.timeout)
			}
		})
	}
}

func TestLiteUnenrollRequiresUsableIdentity(t *testing.T) {
	preserveLiteUnenrollmentHooks(t)
	wantErr := errors.New("firmware unavailable")
	for _, tc := range []struct {
		name        string
		connectErr  error
		identity    *liteclient.DeviceIdentity
		identityErr error
		want        string
	}{
		{name: "connection failed", connectErr: wantErr, want: "connecting to Wendy Lite device"},
		{name: "identity failed", identityErr: wantErr, want: "reading Wendy Lite device identity"},
		{name: "missing identity", want: "returned no device ID"},
		{name: "empty device ID", identity: &liteclient.DeviceIdentity{}, want: "returned no device ID"},
		{name: "blank device ID", identity: &liteclient.DeviceIdentity{ID: " \t"}, want: "returned no device ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := &fakeLiteUnenrollmentDevice{identity: tc.identity, err: tc.identityErr}
			connectLiteUnenrollmentFn = func(models.ExternalDevice) (liteUnenrollmentDevice, error) {
				if tc.connectErr != nil {
					return nil, tc.connectErr
				}
				return device, nil
			}
			unenrollLiteCloudFn = func(*cobra.Command, string, string, string, bool) error {
				t.Fatal("attempted cloud removal without the device identity")
				return nil
			}
			err := runLiteDeviceUnenroll(&cobra.Command{}, models.ExternalDevice{}, "", false)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "--cloud-only --device <asset-id-or-name>") {
				t.Fatalf("missing error or cloud recovery instructions: %v", err)
			}
			if (tc.connectErr != nil || tc.identityErr != nil) && !errors.Is(err, wantErr) {
				t.Fatalf("underlying error lost: %v", err)
			}
			if tc.connectErr == nil && !device.closed {
				t.Fatal("device connection was not closed")
			}
		})
	}
}

func TestLiteUnenrollPreservesCloudConfirmation(t *testing.T) {
	preserveLiteUnenrollmentHooks(t)
	connectLiteUnenrollmentFn = func(models.ExternalDevice) (liteUnenrollmentDevice, error) {
		return &fakeLiteUnenrollmentDevice{identity: &liteclient.DeviceIdentity{ID: "441bf6804824"}}, nil
	}
	called := false
	unenrollLiteCloudFn = func(_ *cobra.Command, cloudGRPC, selector, deviceID string, assumeYes bool) error {
		called = true
		if assumeYes || cloudGRPC != "" {
			t.Fatal("local Lite path bypassed confirmation or changed the selected cloud")
		}
		return nil
	}
	if err := runLiteDeviceUnenroll(&cobra.Command{}, models.ExternalDevice{}, "", false); err != nil || !called {
		t.Fatalf("cloud removal failed: called=%v err=%v", called, err)
	}
}
