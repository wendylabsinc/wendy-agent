package bluetooth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestErrDeviceNotFoundUnwraps(t *testing.T) {
	wrapped := fmt.Errorf("device AA:BB:CC:DD:EE:FF: %w", ErrDeviceNotFound)
	if !errors.Is(wrapped, ErrDeviceNotFound) {
		t.Fatal("wrapped ErrDeviceNotFound must satisfy errors.Is")
	}
}

func TestFriendlyBluetoothError(t *testing.T) {
	tests := []struct {
		name        string
		errName     string
		errMessage  string
		wantSubstr  string // required substring of the friendly text ("" = text must equal errMessage)
		wantNotFund bool
		wantOK      bool
	}{
		// Missing device object → notFound (the "Method Pair ... doesn't exist" failure).
		{"unknown method", "org.freedesktop.DBus.Error.UnknownMethod",
			`Method "Pair" with signature "" on interface "org.bluez.Device1" doesn't exist`,
			"rescan", true, true},
		{"unknown object", "org.freedesktop.DBus.Error.UnknownObject", "", "rescan", true, true},
		{"unknown interface", "org.freedesktop.DBus.Error.UnknownInterface", "", "rescan", true, true},
		{"does not exist", "org.bluez.Error.DoesNotExist", "Does Not Exist", "rescan", true, true},

		// org.bluez.Error.Failed with bearer reason strings.
		{"br page timeout", "org.bluez.Error.Failed", "br-connection-page-timeout", "did not respond", false, true},
		{"br timeout", "org.bluez.Error.Failed", "br-connection-timeout", "did not respond", false, true},
		{"le timeout", "org.bluez.Error.Failed", "le-connection-timeout", "in range", false, true},
		{"br refused", "org.bluez.Error.Failed", "br-connection-refused", "pairing mode", false, true},
		{"br aborted by remote", "org.bluez.Error.Failed", "br-connection-aborted-by-remote", "refused", false, true},
		{"le refused", "org.bluez.Error.Failed", "le-connection-refused", "refused", false, true},
		{"br unknown", "org.bluez.Error.Failed", "br-connection-unknown", "pairing mode", false, true},
		{"le unknown", "org.bluez.Error.Failed", "le-connection-unknown", "pairing mode", false, true},
		{"br adapter off", "org.bluez.Error.Failed", "br-connection-adapter-not-powered", "powered off", false, true},
		{"le adapter off", "org.bluez.Error.Failed", "le-connection-adapter-not-powered", "powered off", false, true},
		{"br busy", "org.bluez.Error.Failed", "br-connection-busy", "retry", false, true},
		{"key missing", "org.bluez.Error.Failed", "br-connection-key-missing", "forget", false, true},
		{"profile unavailable", "org.bluez.Error.Failed", "br-connection-profile-unavailable", "profile", false, true},
		{"sdp search", "org.bluez.Error.Failed", "br-connection-sdp-search", "profile", false, true},
		{"br canceled", "org.bluez.Error.Failed", "br-connection-canceled", "canceled", false, true},
		{"le abort by local", "org.bluez.Error.Failed", "le-connection-abort-by-local", "canceled", false, true},
		// Unrecognized bearer reason → generic hint embedding the raw reason.
		{"br future reason", "org.bluez.Error.Failed", "br-connection-frobnicated", "br-connection-frobnicated", false, true},
		{"le future reason", "org.bluez.Error.Failed", "le-connection-frobnicated", "pairing mode", false, true},
		// Failed without a bearer reason → raw message passthrough.
		{"failed passthrough", "org.bluez.Error.Failed", "Input/output error", "", false, true},

		// Pairing errors.
		{"auth failed", "org.bluez.Error.AuthenticationFailed", "", "pairing mode", false, true},
		{"auth rejected", "org.bluez.Error.AuthenticationRejected", "", "unpair", false, true},
		{"auth canceled", "org.bluez.Error.AuthenticationCanceled", "", "canceled", false, true},
		{"auth timeout", "org.bluez.Error.AuthenticationTimeout", "", "pairing mode", false, true},
		{"pair conn attempt failed", "org.bluez.Error.ConnectionAttemptFailed", "", "in range", false, true},
		{"in progress", "org.bluez.Error.InProgress", "", "retry", false, true},
		{"not ready", "org.bluez.Error.NotReady", "", "adapter", false, true},

		// Bus-level failures.
		{"no reply", "org.freedesktop.DBus.Error.NoReply", "", "did not respond", false, true},
		{"service unknown", "org.freedesktop.DBus.Error.ServiceUnknown", "", "not running", false, true},

		// Unclassified name → ok=false, caller keeps the raw error.
		{"unknown name", "org.bluez.Error.NotSupported", "Operation is not supported", "", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, notFound, ok := friendlyBluetoothError(tt.errName, tt.errMessage)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (text=%q)", ok, tt.wantOK, text)
			}
			if !ok {
				return
			}
			if notFound != tt.wantNotFund {
				t.Errorf("notFound = %v, want %v", notFound, tt.wantNotFund)
			}
			if tt.wantSubstr == "" {
				if text != tt.errMessage {
					t.Errorf("text = %q, want raw message passthrough %q", text, tt.errMessage)
				}
			} else if !strings.Contains(strings.ToLower(text), strings.ToLower(tt.wantSubstr)) {
				t.Errorf("text = %q, want substring %q", text, tt.wantSubstr)
			}
		})
	}
}
