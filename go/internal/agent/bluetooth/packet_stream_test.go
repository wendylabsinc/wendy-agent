package bluetooth

import (
	"bytes"
	"errors"
	"io"
	"syscall"
	"testing"
	"time"
)

type fakePackets struct {
	reads                         [][]byte
	writes                        [][]byte
	readInterrupt, writeInterrupt bool
	readErr                       error
	shortWrite                    bool
}

func (f *fakePackets) Read(p []byte) (int, error) {
	if f.readInterrupt {
		f.readInterrupt = false
		return 0, syscall.EINTR
	}
	if len(f.reads) == 0 {
		return 0, io.EOF
	}
	b := f.reads[0]
	f.reads = f.reads[1:]
	return copy(p, b), f.readErr
}
func (f *fakePackets) Write(p []byte) (int, error) {
	if f.writeInterrupt {
		f.writeInterrupt = false
		return 0, syscall.EINTR
	}
	if f.shortWrite {
		return len(p) - 1, nil
	}
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}
func (*fakePackets) Close() error                     { return nil }
func (*fakePackets) SetDeadline(time.Time) error      { return nil }
func (*fakePackets) SetReadDeadline(time.Time) error  { return nil }
func (*fakePackets) SetWriteDeadline(time.Time) error { return nil }

func TestPacketStreamPreservesSDUsAndSegmentsWrites(t *testing.T) {
	data := bytes.Repeat([]byte("certificates"), 1000)
	p := &fakePackets{reads: [][]byte{data}, readInterrupt: true, writeInterrupt: true}
	c := &packetStreamConn{packet: p, sendMTU: 64}
	var got bytes.Buffer
	buf := make([]byte, 17)
	for {
		n, err := c.Read(buf)
		got.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got.Bytes(), data) {
		t.Fatal("partial TLS reads lost SDU bytes")
	}
	if n, err := c.Write(data); err != nil || n != len(data) {
		t.Fatalf("write=%d,%v", n, err)
	}
	for _, packet := range p.writes {
		if len(packet) > 64 {
			t.Fatalf("SDU %d exceeds negotiated MTU", len(packet))
		}
	}
	if !bytes.Equal(bytes.Join(p.writes, nil), data) {
		t.Fatal("segmented TLS write changed bytes")
	}
}

func TestPacketStreamReadPreservesTrailingError(t *testing.T) {
	p := &fakePackets{reads: [][]byte{[]byte("abcdef")}, readErr: io.EOF}
	c := &packetStreamConn{packet: p, sendMTU: 64}
	buf := make([]byte, 3)
	if n, err := c.Read(buf); n != 3 || err != nil || string(buf) != "abc" {
		t.Fatalf("first read %d,%v,%q", n, err, buf)
	}
	if n, err := c.Read(buf); n != 3 || !errors.Is(err, io.EOF) || string(buf) != "def" {
		t.Fatalf("last read %d,%v,%q", n, err, buf)
	}
}

func TestPacketStreamRejectsShortSDUWrite(t *testing.T) {
	c := &packetStreamConn{packet: &fakePackets{shortWrite: true}, sendMTU: 64}
	if n, err := c.Write([]byte("hello")); n != 4 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("write=%d,%v", n, err)
	}
}
