//go:build linux

package bluetooth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/wendylabsinc/wendy/go/internal/agent/interceptor"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"go.uber.org/zap"
	"golang.org/x/sys/unix"
)

// BT_SNDMTU is a uint16 socket option from linux/bluetooth.h. It is the
// peer's negotiated maximum SDU size, not the controller's link-layer MTU.
const btSendMTU = 12

func negotiatedSendMTU(fd int) (int, error) {
	var mtu uint16
	size := uint32(unsafe.Sizeof(mtu))
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), unix.SOL_BLUETOOTH,
		btSendMTU, uintptr(unsafe.Pointer(&mtu)), uintptr(unsafe.Pointer(&size)), 0)
	if errno != 0 {
		return 0, errno
	}
	if size != 2 || mtu == 0 {
		return 0, fmt.Errorf("invalid negotiated L2CAP SDU size %d", mtu)
	}
	return int(mtu), nil
}

// os.File, unlike net.FileConn, supports these nonblocking AF_BLUETOOTH
// descriptors without interpreting their addresses. Its runtime poller handles
// deadlines, EINTR and concurrent Close without reusing a closed descriptor.
func newL2CAPStreamConn(fd int, addr unix.Sockaddr) (net.Conn, error) {
	mtu, err := negotiatedSendMTU(fd)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("read L2CAP send MTU: %w", err)
	}
	f := os.NewFile(uintptr(fd), "wendy-l2cap")
	if f == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("wrap L2CAP descriptor")
	}
	if err := f.SetDeadline(time.Time{}); err != nil {
		f.Close()
		return nil, fmt.Errorf("L2CAP deadlines unavailable: %w", err)
	}
	remote := "remote"
	if a, ok := addr.(*unix.SockaddrL2); ok {
		b := a.Addr
		remote = fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", b[0], b[1], b[2], b[3], b[4], b[5])
	}
	return &packetStreamConn{packet: f, sendMTU: mtu, remote: l2capAddr(remote)}, nil
}

// startL2CAPServer returns a synchronous stop function. Closing the listener
// precedes draining sessions so a new enrolled identity can safely bind its PSM.
func startL2CAPServer(parent context.Context, logger *zap.Logger, d *Dispatcher, tlsConfig *tls.Config, expected certs.Scope, mode interceptor.OrgMode) (func() error, <-chan struct{}, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.BTPROTO_L2CAP)
	if err != nil {
		return nil, nil, fmt.Errorf("l2cap socket: %w", err)
	}
	sa := &unix.SockaddrL2{PSM: agentinfo.DefaultPSM, AddrType: unix.BDADDR_LE_PUBLIC}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("l2cap bind: %w", err)
	}
	if err := unix.Listen(fd, maxBLEConnections); err != nil {
		unix.Close(fd)
		return nil, nil, fmt.Errorf("l2cap listen: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var sessions sync.WaitGroup
	slots := make(chan struct{}, maxBLEConnections)
	go func() {
		defer close(done)
		defer cancel()
		defer unix.Close(fd)
		for ctx.Err() == nil {
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(pfd, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || pfd[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				logger.Warn("BLE L2CAP listener stopped", zap.Error(err))
				return
			}
			if n == 0 || ctx.Err() != nil {
				continue
			}
			connFD, addr, err := unix.Accept4(fd, unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				continue
			}
			if err != nil {
				logger.Warn("BLE L2CAP accept failed", zap.Error(err))
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				unix.Close(connFD)
				continue
			}
			conn, err := newL2CAPStreamConn(connFD, addr)
			if err != nil {
				<-slots
				logger.Warn("BLE L2CAP connection unavailable", zap.Error(err))
				continue
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer func() { <-slots }()
				serveBLESession(ctx, conn, logger, d.Dispatch, tlsConfig, expected, mode, defaultBLESessionLimits)
			}()
		}
	}()
	logger.Info("BLE L2CAP server listening", zap.Uint16("psm", agentinfo.DefaultPSM))
	var once sync.Once
	var stopErr error
	return func() error {
		once.Do(func() {
			cancel()
			<-done
			drained := make(chan struct{})
			go func() { sessions.Wait(); close(drained) }()
			select {
			case <-drained:
			case <-time.After(5 * time.Second):
				stopErr = fmt.Errorf("BLE handlers did not stop after cancellation")
			}
		})
		return stopErr
	}, done, nil
}
