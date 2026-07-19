//go:build !linux

package services

import "errors"

// usbPresentDetail is unavailable off Linux; the watch alert loop skips
// rounds rather than alerting on devices it cannot see.
func usbPresentDetail() ([]presentUSBDevice, error) {
	return nil, errors.New("usb presence scan is only available on linux")
}
