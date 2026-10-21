//go:build linux

package mesh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"golang.org/x/sys/unix"
)

// udpReplySender owns one send-only IPv4 socket for the proxy. A UDP socket
// bound to the remote VIP's source port would conflict with an unrelated host
// listener, including the proxy itself. Raw replies claim no UDP ports and
// cannot capture later requests. The host agent already runs as root for CNI
// and TPROXY; no app receives CAP_NET_RAW or access to this socket.
type udpReplySender struct {
	conn *net.IPConn
	raw  syscall.RawConn
}

func newUDPReplySender() (*udpReplySender, error) {
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		err := raw.Control(func(fd uintptr) {
			sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_HDRINCL, 1)
		})
		return errors.Join(err, sockErr)
	}}
	// IPPROTO_RAW is send-only on Linux. IP_HDRINCL makes the fixed source
	// address and port explicit without binding a receive socket at the VIP.
	pc, err := lc.ListenPacket(context.Background(), "ip4:255", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("opening net UDP reply sender: %w", err)
	}
	conn := pc.(*net.IPConn)
	raw, err := conn.SyscallConn()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &udpReplySender{conn: conn, raw: raw}, nil
}

// Send must be called inside the source-incarnation authorization callback.
// MSG_DONTWAIT bounds the syscall even under socket pressure; congestion and
// closure are returned honestly rather than waiting while holding admission.
// RawConn protects the file descriptor against concurrent close and reuse.
func (s *udpReplySender) Send(payload []byte, source, destination netip.AddrPort) error {
	if s == nil {
		return net.ErrClosed
	}
	packet, err := udpReplyPacket(payload, source, destination)
	if err != nil {
		return err
	}
	var sendErr error
	err = s.raw.Control(func(fd uintptr) {
		sendErr = unix.Sendto(int(fd), packet, unix.MSG_DONTWAIT, &unix.SockaddrInet4{Addr: destination.Addr().As4()})
	})
	if err = errors.Join(err, sendErr); err != nil {
		return fmt.Errorf("sending net UDP reply: %w", err)
	}
	return nil
}

func (s *udpReplySender) Close() error { return s.conn.Close() }
