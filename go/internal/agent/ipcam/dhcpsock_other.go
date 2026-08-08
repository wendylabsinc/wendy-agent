//go:build !linux

package ipcam

import (
	"context"
	"errors"
	"net"
)

// Serving DHCP needs SO_BINDTODEVICE so a reply can never escape onto the uplink.
// That is Linux-only, and WendyOS is the only place a camera link exists, so the
// other platforms get honest stubs rather than an unbound socket.
//
// The macOS agent builds against these: it can still list and stream cameras that
// already have an address, which is everything except the directly-cabled case.

var errLinkUnsupported = errors.New("camera link configuration is only supported on Linux")

func watchDHCP(_ context.Context, _ string, _ func(*Packet)) error {
	return errLinkUnsupported
}

func serveDHCP(_ context.Context, _ string, _ CameraSegment, _ *LeasePool, _ func(net.HardwareAddr, net.IP, string)) error {
	return errLinkUnsupported
}

func addLinkAddress(_, _ string) error {
	return errLinkUnsupported
}

func delLinkAddress(_, _ string) error {
	return errLinkUnsupported
}
