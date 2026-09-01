package ble

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// DefaultL2CAPPSM is the PSM both the WendyOS agent and the Wendy Lite firmware
// listen on. It is the fallback when a device's PSM is not known from discovery.
const DefaultL2CAPPSM uint16 = 128

// ErrRecvTimeout reports that no L2CAP data arrived within the timeout passed to
// (*Connection).L2CAPRecv. It is a sentinel, not a failure: the channel is still
// open, and a caller with no deadline of its own is expected to retry.
var ErrRecvTimeout = errors.New("BLE L2CAP receive timeout")

// recvChunkSeconds bounds one L2CAPRecv call. It is polling granularity, not a
// timeout any caller sees: Read retries until its own deadline expires, or
// forever if it has none. It also bounds how long Close waits for an in-flight
// Read to come back out of the platform layer, so keep it small.
const recvChunkSeconds = 2

// timeoutSeconds converts a duration to the whole seconds the platform layer
// takes, rounding up and never returning less than 1. Both backends read a
// 0-second timeout as "return immediately" — dispatch_semaphore_wait with
// DISPATCH_TIME_NOW on darwin, poll(2) with 0ms on Linux — so truncating a
// sub-second budget to 0 would turn a short wait into a hot spin. A negative
// value would make poll(2) block forever.
func timeoutSeconds(d time.Duration) int {
	if d <= 0 {
		return 1
	}
	return int((d + time.Second - 1) / time.Second)
}

// NewL2CAPStream presents an open L2CAP channel as a net.Conn, which is all
// crypto/tls and the framing above it need. Call it after OpenL2CAP. Closing the
// returned conn closes the underlying BLE connection.
//
// The channel is a byte stream, not a datagram sequence: CoreBluetooth delivers
// it through an NSStream and drops SDU boundaries, so nothing above may assume
// one Read is one SDU. Leftover bytes from an oversized read are buffered for
// subsequent Read calls.
func NewL2CAPStream(c *Connection) net.Conn {
	return &l2capNetConn{conn: c}
}

type l2capNetConn struct {
	conn         *Connection
	buf          []byte     // leftover from a previous L2CAPRecv
	readDeadline time.Time  // zero means no deadline; owned by the single reader
	recvMu       sync.Mutex // held across L2CAPRecv so Close can't free under it
	closed       atomic.Bool
}

// Read blocks until data arrives, the deadline expires, or the channel closes.
// With no deadline set it waits indefinitely, as net.Conn requires: the
// platform recv is polled in recvChunkSeconds slices and a slice that expires
// with nothing to show is retried rather than reported.
func (c *l2capNetConn) Read(b []byte) (int, error) {
	if len(c.buf) > 0 {
		n := copy(b, c.buf)
		c.buf = c.buf[n:]
		return n, nil
	}

	for {
		chunk := recvChunkSeconds
		if !c.readDeadline.IsZero() {
			remaining := time.Until(c.readDeadline)
			if remaining <= 0 {
				return 0, &timeoutErr{}
			}
			if secs := timeoutSeconds(remaining); secs < chunk {
				chunk = secs
			}
		}

		data, err := c.recv(chunk)
		if errors.Is(err, ErrRecvTimeout) {
			// An idle channel is not a dead one. With no deadline the caller
			// asked to wait indefinitely — liteclient's read loop sits here
			// between device events — so keep waiting; with a deadline, the
			// check above ends it.
			continue
		}
		if err != nil {
			return 0, err
		}
		if len(data) == 0 {
			return 0, io.EOF
		}

		n := copy(b, data)
		if n < len(data) {
			c.buf = append([]byte(nil), data[n:]...)
		}
		return n, nil
	}
}

// recv runs one L2CAPRecv under recvMu so Close cannot tear the platform
// connection down while the call is still inside it. On darwin
// wendy_ble_disconnect hands the CoreBluetooth wrapper to ARC, which releases
// it — while a blocked reader still holds a bare pointer to the same object.
func (c *l2capNetConn) recv(chunkSeconds int) ([]byte, error) {
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	return c.conn.L2CAPRecv(chunkSeconds)
}

func (c *l2capNetConn) Write(b []byte) (int, error) {
	if err := c.conn.L2CAPSend(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close is idempotent: Connection.Close is not (on Linux it closes a bare fd
// without clearing it, so a second call would close an unrelated descriptor).
// It waits out an in-flight Read, which takes at most recvChunkSeconds.
func (c *l2capNetConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.recvMu.Lock()
	defer c.recvMu.Unlock()
	c.conn.Close()
	return nil
}

func (c *l2capNetConn) LocalAddr() net.Addr  { return bleNetAddr{} }
func (c *l2capNetConn) RemoteAddr() net.Addr { return bleNetAddr{} }

func (c *l2capNetConn) SetDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}
func (c *l2capNetConn) SetReadDeadline(t time.Time) error {
	c.readDeadline = t
	return nil
}
func (c *l2capNetConn) SetWriteDeadline(_ time.Time) error { return nil }

// bleNetAddr is a minimal net.Addr for the BLE transport.
type bleNetAddr struct{}

func (bleNetAddr) Network() string { return "ble-l2cap" }
func (bleNetAddr) String() string  { return "ble" }

// timeoutErr is returned when a read deadline is exceeded.
type timeoutErr struct{}

func (e *timeoutErr) Error() string   { return "BLE L2CAP: i/o timeout" }
func (e *timeoutErr) Timeout() bool   { return true }
func (e *timeoutErr) Temporary() bool { return true }

// NewClientTLSConfig builds a *tls.Config for the BLE client.
// InsecureSkipVerify bypasses Go's built-in verifier (ML-DSA chain certs
// fail to parse; no TLS hostname over L2CAP); opts.PinStore and chain
// verification are handled by the VerifyConnection callback.
func NewClientTLSConfig(certPEM, keyPEM string, opts certs.ServerVerifyOpts) (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("loading BLE client certificate: %w", err)
	}
	verifyConn, err := certs.BuildServerVerifyConnection(opts)
	if err != nil {
		return nil, fmt.Errorf("building BLE server certificate verifier: %w", err)
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, //nolint:gosec — hostname bypass only; VerifyConnection validates server cert against Wendy PKI
		VerifyConnection:   verifyConn,
		MinVersion:         tls.VersionTLS12,
	}, nil
}
