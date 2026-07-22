//go:build !linux

package mesh

import (
	"errors"
	"net"
	"net/netip"
)

func originalDst(*net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("mesh: SO_ORIGINAL_DST is only available on linux")
}
