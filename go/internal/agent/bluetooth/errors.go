package bluetooth

import (
	"errors"
	"strings"
)

// ErrDeviceNotFound indicates the peripheral is not known to BlueZ and was not
// seen during on-demand discovery. Services map it to codes.NotFound so the
// CLI can tell "rescan" apart from a genuine connection failure.
var ErrDeviceNotFound = errors.New("bluetooth device not found")

// friendlyBluetoothError converts a D-Bus/BlueZ error, identified by its D-Bus
// error name and message body, into user-facing text. notFound reports that
// the error means the device object does not exist in BlueZ; ok=false means
// the name was not recognized and the caller should keep the raw error.
//
// This file carries no build tag (and must stay free of godbus imports) so the
// services package can classify errors on every platform and the tests run on
// non-Linux dev machines.
func friendlyBluetoothError(name, message string) (text string, notFound, ok bool) {
	switch name {
	case "org.freedesktop.DBus.Error.UnknownMethod",
		"org.freedesktop.DBus.Error.UnknownObject",
		"org.freedesktop.DBus.Error.UnknownInterface",
		"org.bluez.Error.DoesNotExist":
		return "device is no longer known to the Bluetooth adapter — make sure it is powered on and in range, then rescan", true, true

	case "org.bluez.Error.Failed":
		return friendlyBearerFailure(message), false, true

	case "org.bluez.Error.AuthenticationFailed":
		return "pairing authentication failed — put the device in pairing mode and retry", false, true
	case "org.bluez.Error.AuthenticationRejected":
		return "the device rejected pairing — it may be bonded to another host; unpair it there, or forget it here and retry", false, true
	case "org.bluez.Error.AuthenticationCanceled":
		return "pairing was canceled by the device", false, true
	case "org.bluez.Error.AuthenticationTimeout":
		return "pairing timed out — make sure the device is in pairing mode and in range", false, true
	case "org.bluez.Error.ConnectionAttemptFailed":
		return "could not reach the device to pair — make sure it is powered on and in range", false, true
	case "org.bluez.Error.InProgress":
		return "another Bluetooth operation is in progress — retry in a few seconds", false, true
	case "org.bluez.Error.NotReady":
		return "the Bluetooth adapter is not ready — check that it is powered on", false, true

	case "org.freedesktop.DBus.Error.NoReply":
		return "the Bluetooth service did not respond in time — retry", false, true
	case "org.freedesktop.DBus.Error.ServiceUnknown":
		return "the Bluetooth service (bluetoothd) is not running on the device", false, true
	}
	return "", false, false
}

// friendlyBearerFailure maps the reason strings BlueZ places in
// org.bluez.Error.Failed messages (src/error.c, e.g. "br-connection-refused")
// to actionable text. Unrecognized bearer reasons get a generic hint that
// embeds the raw reason; messages without a bearer reason pass through
// unchanged (older BlueZ reports plain strerror text there).
func friendlyBearerFailure(message string) string {
	has := func(reasons ...string) bool {
		for _, r := range reasons {
			if strings.Contains(message, r) {
				return true
			}
		}
		return false
	}

	switch {
	case has("br-connection-page-timeout", "br-connection-timeout", "le-connection-timeout"):
		return "the device did not respond — make sure it is powered on and in range"
	case has("br-connection-refused", "br-connection-aborted-by-remote", "le-connection-refused"):
		return "the device refused the connection — put it in pairing mode; if it is paired to another host, disconnect or unpair it there first"
	case has("br-connection-unknown", "le-connection-unknown"):
		return "the device rejected or dropped the connection — put it in pairing mode and retry; if it is bonded to another device, unpair it there first"
	case has("br-connection-adapter-not-powered", "le-connection-adapter-not-powered"):
		return "the Bluetooth adapter is powered off"
	case has("br-connection-busy", "le-connection-busy"):
		return "the Bluetooth adapter is busy — wait a few seconds and retry"
	case has("br-connection-key-missing"):
		return "stored pairing keys are stale — forget the device and pair again"
	case has("br-connection-profile-unavailable", "br-connection-sdp-search"):
		return "no usable service profile found on the device — put it in pairing mode and retry"
	case has("br-connection-canceled", "le-connection-abort-by-local"):
		return "the connection attempt was canceled"
	case has("br-connection-", "le-connection-"):
		return "connection failed (" + message + ") — make sure the device is in pairing mode and in range"
	}
	return message
}
