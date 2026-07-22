package adbproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeADBD is an in-memory adbd that speaks the wire protocol back to a Conn: the
// host's Writes are parsed into messages and responses are queued for its Reads.
// Responses are generated synchronously on Write, so a subsequent Read always
// finds them. It deliberately coalesces all queued bytes into one Read to
// exercise the Conn's read-buffer reassembly.
type fakeADBD struct {
	t *testing.T

	fromHost []byte // bytes the host has written, awaiting parse
	toHost   []byte // bytes queued for the host to read

	banner      string
	shellStdout []byte
	shellExit   int

	pushFail        string // if set, DONE is answered with FAIL+this message
	resultBeforeAck bool   // send the sync result WRTE before the DONE flow-OKAY

	nextRemote  uint32
	syncLocal   uint32 // host stream id of the open sync stream
	syncRemote  uint32 // our stream id for it
	sawShell    bool
	sawPushDone bool
}

func (f *fakeADBD) Read(p []byte, _ time.Duration) (int, error) {
	if len(f.toHost) == 0 {
		return 0, errors.New("fakeADBD: host read with nothing queued (protocol desync?)")
	}
	n := copy(p, f.toHost)
	f.toHost = f.toHost[n:]
	return n, nil
}

func (f *fakeADBD) Write(p []byte) error {
	f.fromHost = append(f.fromHost, p...)
	for len(f.fromHost) >= 24 {
		dlen := binary.LittleEndian.Uint32(f.fromHost[12:])
		if uint32(len(f.fromHost)) < 24+dlen {
			break
		}
		cmd := binary.LittleEndian.Uint32(f.fromHost[0:])
		a0 := binary.LittleEndian.Uint32(f.fromHost[4:])
		a1 := binary.LittleEndian.Uint32(f.fromHost[8:])
		payload := append([]byte(nil), f.fromHost[24:24+dlen]...)
		f.fromHost = f.fromHost[24+dlen:]
		f.handle(cmd, a0, a1, payload)
	}
	return nil
}

func (f *fakeADBD) send(cmd, arg0, arg1 uint32, data []byte) {
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], cmd)
	binary.LittleEndian.PutUint32(hdr[4:], arg0)
	binary.LittleEndian.PutUint32(hdr[8:], arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(hdr[20:], cmd^0xffffffff)
	f.toHost = append(f.toHost, hdr...)
	f.toHost = append(f.toHost, data...)
}

func (f *fakeADBD) handle(cmd, a0, a1 uint32, payload []byte) {
	switch cmd {
	case cmdCNXN:
		f.send(cmdCNXN, adbVersion, maxPayload, []byte(f.banner))
	case cmdOPEN:
		f.nextRemote++
		remote := 100 + f.nextRemote
		service := strings.TrimRight(string(payload), "\x00")
		f.send(cmdOKAY, remote, a0, nil) // accept the stream
		switch {
		case strings.HasPrefix(service, "shell,v2,raw:"):
			f.sawShell = true
			var frames []byte
			frames = appendShellFrame(frames, shellStdout, f.shellStdout)
			frames = appendShellFrame(frames, shellExit, []byte{byte(f.shellExit)})
			f.send(cmdWRTE, remote, a0, frames)
			f.send(cmdCLSE, remote, a0, nil)
		case service == "sync:":
			f.syncLocal = a0
			f.syncRemote = remote
		}
	case cmdWRTE:
		// Host data on an open stream. Only the sync stream needs handling.
		if a1 != f.syncRemote {
			return
		}
		sub := ""
		if len(payload) >= 4 {
			sub = string(payload[0:4])
		}
		switch sub {
		case "DONE":
			f.sawPushDone = true
			result := []byte("OKAY\x00\x00\x00\x00")
			if f.pushFail != "" {
				result = syncReq("FAIL", []byte(f.pushFail))
			}
			if f.resultBeforeAck {
				f.send(cmdWRTE, f.syncRemote, f.syncLocal, result) // result first
				f.send(cmdOKAY, f.syncRemote, f.syncLocal, nil)    // then flow-ack
			} else {
				f.send(cmdOKAY, f.syncRemote, f.syncLocal, nil)
				f.send(cmdWRTE, f.syncRemote, f.syncLocal, result)
			}
		default: // SEND, DATA
			f.send(cmdOKAY, f.syncRemote, f.syncLocal, nil)
		}
	case cmdCLSE, cmdOKAY:
		// Host closing a stream / acking our WRTE: nothing to reply.
	}
}

func appendShellFrame(dst []byte, id byte, payload []byte) []byte {
	hdr := make([]byte, 5)
	hdr[0] = id
	binary.LittleEndian.PutUint32(hdr[1:], uint32(len(payload)))
	dst = append(dst, hdr...)
	return append(dst, payload...)
}

const goodBanner = "device::ro.product.name=thor;features=cmd,shell_v2,stat_v2"

func TestConnectRequiresShellV2(t *testing.T) {
	c := NewConn(&fakeADBD{t: t, banner: "device::features=cmd,stat_v2"})
	if err := c.Connect(); err == nil {
		t.Fatal("Connect should fail when the banner lacks shell_v2")
	}
	c = NewConn(&fakeADBD{t: t, banner: goodBanner})
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect with shell_v2 banner: %v", err)
	}
}

func TestShellSuccessAndExit(t *testing.T) {
	f := &fakeADBD{t: t, banner: goodBanner, shellStdout: []byte("hello world\n"), shellExit: 0}
	c := NewConn(f)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	out, err := c.Shell("echo hello world")
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if out != "hello world\n" {
		t.Fatalf("stdout = %q", out)
	}

	f2 := &fakeADBD{t: t, banner: goodBanner, shellStdout: []byte("boom\n"), shellExit: 7}
	c2 := NewConn(f2)
	if err := c2.Connect(); err != nil {
		t.Fatal(err)
	}
	_, err = c2.Shell("false")
	var ee *ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *ExitError, got %T (%v)", err, err)
	}
	if ee.Code != 7 {
		t.Fatalf("exit code = %d, want 7", ee.Code)
	}
}

func TestPushSuccess(t *testing.T) {
	f := &fakeADBD{t: t, banner: goodBanner}
	c := NewConn(f)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	// A payload larger than one 64 KiB sync chunk, to exercise multi-chunk streaming.
	data := bytes.Repeat([]byte("x"), 200*1024)
	if err := c.Push(bytes.NewReader(data), "/tmp/blob", 0o644); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !f.sawPushDone {
		t.Fatal("device never saw DONE")
	}
}

// TestPushResultBeforeAck locks in the fix for the interleaved-WRTE bug: the sync
// result WRTE may arrive before the flow-control OKAY for our DONE. The old code
// discarded it and hung; Push must handle either ordering.
func TestPushResultBeforeAck(t *testing.T) {
	f := &fakeADBD{t: t, banner: goodBanner, resultBeforeAck: true}
	c := NewConn(f)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := c.Push(bytes.NewReader([]byte("small")), "/tmp/blob", 0o644); err != nil {
		t.Fatalf("Push with result-before-ack ordering: %v", err)
	}
}

func TestPushFailSurfaced(t *testing.T) {
	f := &fakeADBD{t: t, banner: goodBanner, pushFail: "No space left on device"}
	c := NewConn(f)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	err := c.Push(bytes.NewReader([]byte("data")), "/tmp/blob", 0o644)
	if err == nil || !strings.Contains(err.Error(), "No space left on device") {
		t.Fatalf("want push rejected error carrying device message, got %v", err)
	}
}

// TestReadCoalescing verifies the Conn reassembles messages when several arrive in
// one bulk read and one is split across reads — the desync class this hardening
// addressed. The fake returns everything queued in a single Read; a second Shell
// exercises back-to-back stream reuse.
func TestReadCoalescing(t *testing.T) {
	f := &fakeADBD{t: t, banner: goodBanner, shellStdout: []byte(strings.Repeat("a", 1000))}
	c := NewConn(f)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		out, err := c.Shell(fmt.Sprintf("cmd%d", i))
		if err != nil {
			t.Fatalf("Shell %d: %v", i, err)
		}
		if len(out) != 1000 {
			t.Fatalf("Shell %d stdout len = %d, want 1000", i, len(out))
		}
	}
}
