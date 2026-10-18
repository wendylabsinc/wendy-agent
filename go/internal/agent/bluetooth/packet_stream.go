package bluetooth

import (
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

type packetFile interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

// packetStreamConn preserves SDU boundaries below a stream interface. A complete
// SDU must be read at once; passing TLS's smaller buffer to a SEQPACKET socket
// would silently discard its remainder. Writes split at the negotiated SDU MTU.
type packetStreamConn struct {
	packet  packetFile
	sendMTU int
	remote  net.Addr
	readMu  sync.Mutex
	writeMu sync.Mutex
	rbuf    []byte
	rerr    error
	readSDU []byte
}

func (c *packetStreamConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(b) == 0 {
		return 0, nil
	}
	if len(c.rbuf) == 0 {
		if c.readSDU == nil {
			c.readSDU = make([]byte, 65536)
		}
		msg := c.readSDU
		var n int
		var err error
		for {
			n, err = c.packet.Read(msg)
			if n > 0 || !errors.Is(err, syscall.EINTR) {
				break
			}
		}
		if n == 0 {
			if err == nil {
				err = io.EOF
			}
			return 0, err
		}
		c.rbuf = msg[:n]
		c.rerr = err
	}
	n := copy(b, c.rbuf)
	c.rbuf = c.rbuf[n:]
	if len(c.rbuf) == 0 {
		err := c.rerr
		c.rerr = nil
		return n, err
	}
	return n, nil
}

func (c *packetStreamConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.sendMTU <= 0 {
		return 0, errors.New("L2CAP send MTU unavailable")
	}
	written := 0
	for written < len(b) {
		chunk := b[written:min(len(b), written+c.sendMTU)]
		n, err := c.packet.Write(chunk)
		if errors.Is(err, syscall.EINTR) && n == 0 {
			continue
		}
		written += n
		if err != nil {
			return written, err
		}
		if n != len(chunk) {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func (c *packetStreamConn) Close() error                       { return c.packet.Close() }
func (c *packetStreamConn) LocalAddr() net.Addr                { return l2capAddr("local") }
func (c *packetStreamConn) RemoteAddr() net.Addr               { return c.remote }
func (c *packetStreamConn) SetDeadline(t time.Time) error      { return c.packet.SetDeadline(t) }
func (c *packetStreamConn) SetReadDeadline(t time.Time) error  { return c.packet.SetReadDeadline(t) }
func (c *packetStreamConn) SetWriteDeadline(t time.Time) error { return c.packet.SetWriteDeadline(t) }

type l2capAddr string

func (a l2capAddr) Network() string { return "l2cap" }
func (a l2capAddr) String() string  { return string(a) }
