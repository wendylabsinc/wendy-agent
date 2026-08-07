//go:build !darwin && !linux

package services

import (
	"errors"
	"io"
)

// IP-camera capture is supported by the Linux WendyOS agent and the macOS
// agent. Refuse elsewhere rather than falling back to a credential-bearing
// gst-launch command line.
func RunIPCameraGStreamerHelper(_ io.Reader, _ io.Writer) error {
	return errors.New("IP camera streaming is only supported on Linux and macOS")
}
