//go:build !linux

package services

import (
	"errors"
	"net"
)

// errPeerCredUnsupported reports that SO_PEERCRED-based peer identity binding
// is unavailable on this platform. verifyPeer treats this as fail-open: the
// group-2000 gate and the app-private socket directory remain the baseline.
var errPeerCredUnsupported = errors.New("SO_PEERCRED peer credential lookup is not supported on this platform")

func readPeerCredentials(net.Conn) (peerCredentials, error) {
	return peerCredentials{}, errPeerCredUnsupported
}

func readProcCgroup(int32) (string, error) {
	return "", errPeerCredUnsupported
}
