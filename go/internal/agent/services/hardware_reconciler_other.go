//go:build !linux

package services

import "errors"

// usbPresentDevices is unavailable off Linux; the reconciler skips rounds
// rather than alerting on devices it cannot see.
func usbPresentDevices() (map[string]bool, error) {
	return nil, errors.New("usb presence scan is only available on linux")
}
