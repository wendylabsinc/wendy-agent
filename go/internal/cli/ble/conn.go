package ble

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
)

// l2capNetConn adapts a *Connection to net.Conn so the TLS client can run over
// the L2CAP channel. Each Read call maps to one L2CAPRecv (one L2CAP SDU),
// which lines up with TLS's one-record-per-Write discipline. Leftover bytes
// from an oversized SDU are buffered for subsequent Read calls.
type l2capNetConn struct {
	conn         *Connection
	buf          []byte    // leftover from a previous L2CAPRecv
	readDeadline time.Time // zero means no deadline
}

func newL2CAPNetConn(c *Connection) net.Conn {
	return &l2capNetConn{conn: c}
}

func (c *l2capNetConn) Read(b []byte) (int, error) {
	if len(c.buf) > 0 {
		n := copy(b, c.buf)
		c.buf = c.buf[n:]
		return n, nil
	}

	timeout := 30
	if !c.readDeadline.IsZero() {
		d := time.Until(c.readDeadline)
		if d <= 0 {
			return 0, &timeoutErr{}
		}
		if secs := int(d.Seconds()) + 1; secs < timeout {
			timeout = secs
		}
	}

	data, err := c.conn.L2CAPRecv(timeout)
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, io.EOF
	}

	n := copy(b, data)
	if n < len(data) {
		c.buf = make([]byte, len(data)-n)
		copy(c.buf, data[n:])
	}
	return n, nil
}

func (c *l2capNetConn) Write(b []byte) (int, error) {
	if err := c.conn.L2CAPSend(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *l2capNetConn) Close() error {
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
