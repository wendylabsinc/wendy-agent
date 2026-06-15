//go:build linux

package bluetooth

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"go.uber.org/zap"
)

func TestPairingAgent_AcceptsOnlyExpectedDevice(t *testing.T) {
	const expected = dbus.ObjectPath("/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF")
	const other = dbus.ObjectPath("/org/bluez/hci0/dev_11_22_33_44_55_66")

	agent := &pairingAgent{logger: zap.NewNop(), expected: expected}

	// The expected device is auto-accepted across all three callbacks.
	if err := agent.RequestConfirmation(expected, 123456); err != nil {
		t.Errorf("RequestConfirmation(expected) = %v, want nil", err)
	}
	if err := agent.RequestAuthorization(expected); err != nil {
		t.Errorf("RequestAuthorization(expected) = %v, want nil", err)
	}
	if err := agent.AuthorizeService(expected, "0000110b-0000-1000-8000-00805f9b34fb"); err != nil {
		t.Errorf("AuthorizeService(expected) = %v, want nil", err)
	}

	// Any other device in range during the pairing window is rejected.
	if err := agent.RequestConfirmation(other, 123456); err != errRejected {
		t.Errorf("RequestConfirmation(other) = %v, want errRejected", err)
	}
	if err := agent.RequestAuthorization(other); err != errRejected {
		t.Errorf("RequestAuthorization(other) = %v, want errRejected", err)
	}
	if err := agent.AuthorizeService(other, "0000110b-0000-1000-8000-00805f9b34fb"); err != errRejected {
		t.Errorf("AuthorizeService(other) = %v, want errRejected", err)
	}
}
