//go:build linux

package mesh

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/sys/unix"
)

// originalDst recovers the pre-REDIRECT destination of a connection that
// arrived via the WENDY-MESH nat REDIRECT rule.
func originalDst(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var (
		addr    netip.AddrPort
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		mreq, err := unix.GetsockoptIPv6Mreq(int(fd), unix.IPPROTO_IP, unix.SO_ORIGINAL_DST)
		if err != nil {
			sockErr = err
			return
		}
		addr = addrPortFromSockaddrIn(mreq.Multiaddr[:])
	}); err != nil {
		return netip.AddrPort{}, err
	}
	if sockErr != nil {
		return netip.AddrPort{}, fmt.Errorf("getsockopt SO_ORIGINAL_DST: %w", sockErr)
	}
	return addr, nil
}
