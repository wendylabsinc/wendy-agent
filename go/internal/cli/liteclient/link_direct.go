package liteclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	wendypb "github.com/wendylabsinc/wendy/go/proto/gen/litepb"
	"go.bug.st/serial"
	"google.golang.org/protobuf/proto"
)

// directLink frames WendyComMessages with the 8-byte link header over a
// direct connection (TCP-TLS or serial). The header channel byte stays 0:
// direct links always use the default channel.
type directLink struct {
	conn     io.ReadWriteCloser
	isSerial bool
	writeMu  sync.Mutex // serializes frames across command goroutines
}

func (l *directLink) send(req *wendypb.WendyComMessage) error {
	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	msg := make([]byte, headerSize+len(body))
	msg[0] = headerMagic
	msg[1] = headerVersion
	binary.BigEndian.PutUint16(msg[6:8], uint16(len(body)))
	copy(msg[headerSize:], body)
	if l.isSerial {
		msg = bytes.ReplaceAll(msg, []byte{esc}, []byte{esc, '_'})
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	for len(msg) > 0 {
		n, err := l.conn.Write(msg)
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
		msg = msg[n:]
	}
	return nil
}

func (l *directLink) recv(timeout time.Duration) (*wendypb.WendyComMessage, error) {
	raw, err := l.readRawMessage(timeout)
	if err != nil {
		return nil, err
	}
	msg := &wendypb.WendyComMessage{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return msg, nil
}

func (l *directLink) maxChunk() int {
	if l.isSerial {
		return chunkSizeForSerial
	}
	return chunkSize
}

func (l *directLink) close() error {
	if l.isSerial {
		if port, ok := l.conn.(serial.Port); ok {
			_, _ = port.Write([]byte{esc, 'o'})
			_ = port.Drain()
		}
	}
	return l.conn.Close()
}

// readRawMessage reads one framed message of any kind (response, event, handshake)
// and returns its raw body. A timeout <= 0 means no deadline.
func (l *directLink) readRawMessage(timeout time.Duration) ([]byte, error) {
	header := make([]byte, headerSize)
	if err := l.readFull(header, timeout); err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}
	if header[0] != headerMagic {
		return nil, fmt.Errorf("unexpected magic byte: 0x%02X", header[0])
	}
	if header[1] != headerVersion {
		return nil, fmt.Errorf("unexpected protocol version: 0x%02X", header[1])
	}
	bodyLen := binary.BigEndian.Uint16(header[6:8])
	if bodyLen == 0 {
		return nil, nil
	}
	body := make([]byte, bodyLen)
	if err := l.readFull(body, timeout); err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}
	return body, nil
}

// readFull reads exactly len(buf) bytes from the connection within timeout.
// A zero timeout means no deadline.
//
// For net.Conn, it sets SetReadDeadline for the duration of the call.
//
// For serial.Port, SetReadTimeout makes Read return (0, nil) on timeout
// instead of an error, which would cause io.ReadFull to spin indefinitely.
// readFull therefore loops manually, trimming the per-Read call to the
// remaining time until the deadline, and converts (0, nil) to an error.
func (l *directLink) readFull(buf []byte, timeout time.Duration) error {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	if !l.isSerial {
		if nc, ok := l.conn.(net.Conn); ok && !deadline.IsZero() {
			_ = nc.SetReadDeadline(deadline)
			defer nc.SetReadDeadline(time.Time{}) //nolint:errcheck
		}
		_, err := io.ReadFull(l.conn, buf)
		return err
	}
	sp := l.conn.(serial.Port)
	defer sp.SetReadTimeout(serial.NoTimeout) //nolint:errcheck
	for len(buf) > 0 {
		perRead := serial.NoTimeout
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("read timeout")
			}
			perRead = remaining
		}
		_ = sp.SetReadTimeout(perRead)
		n, err := sp.Read(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("read timeout")
		}
		buf = buf[n:]
	}
	return nil
}
