//go:build linux

package meshsession

import (
	"context"
	"net"
	"syscall"

	"github.com/wendylabsinc/wendy/go/internal/agent/localmesh"
	"golang.org/x/sys/unix"
)

func listenAppPackets(ctx context.Context, credentials *localmesh.Credentials, addr string) (net.PacketConn, error) {
	address, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	if address.IP.IsUnspecified() || len(address.IP) == 0 {
		address.IP = net.IP(credentials.Device.MeshAddress().AsSlice())
	}
	// A wildcard quic-go listener caches the first packet's ingress ifindex in
	// PKTINFO for all replies. Babel can change TUNs without changing either
	// UDP endpoint; replies then use the old link and fail its forwarding gate.
	// Bind the device's own mesh address instead, retaining native UDP
	// batching/GSO/ECN while allowing each send to follow the current kernel
	// route.
	lc := net.ListenConfig{}
	network, level, freebind := "udp6", unix.IPPROTO_IPV6, unix.IPV6_FREEBIND
	if address.IP.To4() != nil {
		network, level, freebind = "udp4", unix.IPPROTO_IP, unix.IP_FREEBIND
	}
	// The session server starts before the mesh host interface carries this
	// device's address and survives that interface's recreation. FREEBIND
	// permits the bind without pinning any interface or changing host-wide
	// nonlocal-bind policy; local delivery still needs the address to exist.
	lc.Control = func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		err := raw.Control(func(fd uintptr) { sockErr = unix.SetsockoptInt(int(fd), level, freebind, 1) })
		if err != nil {
			return err
		}
		return sockErr
	}
	return lc.ListenPacket(ctx, network, address.String())
}
