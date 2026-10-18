//go:build linux

package bluetooth

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func seqpacketPair(t *testing.T) (*packetStreamConn, *packetStreamConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(fd int) *packetStreamConn {
		f := os.NewFile(uintptr(fd), "test-seqpacket")
		if f == nil {
			unix.Close(fd)
			t.Fatal("wrap seqpacket")
		}
		t.Cleanup(func() { f.Close() })
		return &packetStreamConn{packet: f, sendMTU: 64, remote: l2capAddr("test")}
	}
	return wrap(fds[0]), wrap(fds[1])
}

func TestPacketStreamRuntimePoller(t *testing.T) {
	t.Run("large stream over small SDUs", func(t *testing.T) {
		a, b := seqpacketPair(t)
		a.SetDeadline(time.Now().Add(time.Second))
		b.SetDeadline(time.Now().Add(time.Second))
		payload := bytes.Repeat([]byte("large TLS certificate record"), 1000)
		done := make(chan error, 1)
		go func() { _, err := a.Write(payload); done <- err }()
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(b, got); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("stream corrupted across SDUs")
		}
	})
	t.Run("absolute read deadline", func(t *testing.T) {
		a, _ := seqpacketPair(t)
		if err := a.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("read error=%v", err)
		}
	})
	t.Run("close interrupts read", func(t *testing.T) {
		a, _ := seqpacketPair(t)
		done := make(chan error, 1)
		go func() { _, err := a.Read(make([]byte, 1)); done <- err }()
		a.Close()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("closed read succeeded")
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not interrupt read")
		}
	})
}
