// Package adbproto speaks the Android Debug Bridge wire protocol (the CNXN
// handshake plus OPEN/OKAY/WRTE/CLSE stream multiplexing, the shell-protocol-v2
// service, and the sync/SEND file push) over an abstract bulk transport, so the
// same protocol code drives NVIDIA's T264 initrd-flash gadget on every platform:
// the gousb bulk endpoints on macOS/Linux (package adb) and WinUSB on Windows
// (package winusb). Only the per-transfer USB read/write differs; everything on
// the wire lives here.
//
// Auth (the on-device "allow this computer" RSA prompt) is not implemented: a
// headless flashing initrd runs adbd insecure, so the device replies to CNXN
// with CNXN rather than AUTH.
package adbproto

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// Transport is one bulk USB pipe pair to the device. Both the gousb (macOS/Linux)
// and WinUSB (Windows) backends implement it.
type Transport interface {
	// Read reads one bulk-IN transfer into p, bounded by timeout, and returns the
	// number of bytes read. A zero-byte read with no error is treated as EOF.
	Read(p []byte, timeout time.Duration) (int, error)
	// Write sends p as bulk-OUT transfers with no spurious end-of-transfer
	// zero-length packet (ADB is length-prefixed; a stray ZLP desyncs the stream).
	Write(p []byte) error
}

// ADB message commands (little-endian 4-byte tags).
const (
	cmdCNXN = 0x4e584e43 // "CNXN"
	cmdAUTH = 0x48545541 // "AUTH"
	cmdOPEN = 0x4e45504f // "OPEN"
	cmdOKAY = 0x59414b4f // "OKAY"
	cmdCLSE = 0x45534c43 // "CLSE"
	cmdWRTE = 0x45545257 // "WRTE"

	adbVersion  = 0x01000001
	maxPayload  = 256 * 1024
	syncDataMax = 64 * 1024

	// IOTimeout bounds each USB transfer. It is large because device-side flash
	// steps (QSPI erase, dd of multi-GB partitions) hold the shell stream open for
	// a long time with no output, and a timed-out bulk-IN read is destructive on
	// macOS (it aborts the endpoint), so we must not time out during a legitimate op.
	IOTimeout = 30 * time.Minute

	// handshakeReadTimeout bounds the CNXN reply read specifically. A healthy adbd
	// answers immediately, so a short bound lets the caller's Open()/retry loop fail
	// fast while the gadget's adbd is still coming up, instead of blocking on the
	// 30-minute IOTimeout.
	handshakeReadTimeout = 3 * time.Second
)

// shell-protocol-v2 stream message ids (1-byte id + 4-byte LE length + payload).
const (
	shellStdout = 1
	shellStderr = 2
	shellExit   = 3
)

// ExitError reports a non-zero shell exit status.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("shell exited with status %d", e.Code) }

// Conn is an ADB session over a Transport. It is single-goroutine: one flash
// drives it sequentially, so there is no internal locking.
type Conn struct {
	t       Transport
	nextID  uint32
	rbuf    []byte // bulk-IN bytes read past a message boundary, not yet consumed
	scratch []byte // reusable read buffer (avoids a per-read allocation)
	// Banner is the device identity string from the CNXN reply.
	Banner string
}

// NewConn wraps a transport. Call Connect before Shell/Push.
func NewConn(t Transport) *Conn { return &Conn{t: t, nextID: 1} }

// Connect performs the CNXN handshake and requires the device to advertise
// shell_v2: the engine's error model depends on Shell reporting non-zero exits,
// and only shell-protocol-v2 frames an exit status (the legacy shell:/exec:
// services return "fork failed" on the T264 flashing gadget anyway), so a device
// without it would make a failed command look like success.
func (c *Conn) Connect() error {
	if err := c.writeMsg(cmdCNXN, adbVersion, maxPayload, []byte("host::\x00")); err != nil {
		return fmt.Errorf("sending CNXN: %w", err)
	}
	for {
		cmd, _, _, data, err := c.readMsg(handshakeReadTimeout)
		if err != nil {
			return fmt.Errorf("reading CNXN reply: %w", err)
		}
		switch cmd {
		case cmdCNXN:
			c.Banner = string(data)
			if !containsFeature(c.Banner, "shell_v2") {
				return fmt.Errorf("device adbd does not advertise shell_v2 (banner %q); required for reliable exit-status reporting", c.Banner)
			}
			return nil
		case cmdAUTH:
			return fmt.Errorf("device requires ADB authentication (secure adbd); not supported")
		}
	}
}

// containsFeature reports whether the CNXN banner's feature list includes name.
// The banner is like "device::...;features=cmd,shell_v2,...".
func containsFeature(banner, name string) bool {
	// A plain substring check is sufficient here: adbd feature names don't overlap
	// as substrings of one another in a way that matters for shell_v2.
	return indexOf(banner, name) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// writeMsg sends one ADB message: a 24-byte header then the payload, as two
// transport writes (adbd reads the header as its own transfer).
func (c *Conn) writeMsg(cmd, arg0, arg1 uint32, data []byte) error {
	var check uint32
	for _, b := range data {
		check += uint32(b)
	}
	hdr := make([]byte, 24)
	binary.LittleEndian.PutUint32(hdr[0:], cmd)
	binary.LittleEndian.PutUint32(hdr[4:], arg0)
	binary.LittleEndian.PutUint32(hdr[8:], arg1)
	binary.LittleEndian.PutUint32(hdr[12:], uint32(len(data)))
	binary.LittleEndian.PutUint32(hdr[16:], check)
	binary.LittleEndian.PutUint32(hdr[20:], cmd^0xffffffff)

	if err := c.t.Write(hdr); err != nil {
		return err
	}
	if len(data) > 0 {
		if err := c.t.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// readMsg reads one ADB message: a 24-byte header then data_length payload bytes.
// The whole message is bounded by timeout (a single deadline shared across the
// underlying reads), so a payload dribbled across several bulk transfers cannot
// extend the effective timeout to a multiple of it.
//
// Bytes read past this message's end are retained in c.rbuf for the next call:
// adbd messages can coalesce into one bulk transfer (a payload that is an exact
// packet-size multiple rides together with the next header), and dropping the
// tail would desync the stream.
func (c *Conn) readMsg(timeout time.Duration) (cmd, arg0, arg1 uint32, data []byte, err error) {
	deadline := time.Now().Add(timeout)
	if err = c.fill(24, deadline, timeout); err != nil {
		return
	}
	hdr := c.rbuf[:24]
	cmd = binary.LittleEndian.Uint32(hdr[0:])
	arg0 = binary.LittleEndian.Uint32(hdr[4:])
	arg1 = binary.LittleEndian.Uint32(hdr[8:])
	dlen := binary.LittleEndian.Uint32(hdr[12:])
	// The length comes from the device; bound it so a malformed/oversized header
	// can't drive an unbounded allocation. ADB payloads never exceed the max.
	if dlen > maxPayload {
		err = fmt.Errorf("ADB payload too large: %d bytes (max %d)", dlen, maxPayload)
		return
	}
	if err = c.fill(24+int(dlen), deadline, timeout); err != nil {
		return
	}
	data = append([]byte(nil), c.rbuf[24:24+dlen]...)
	c.rbuf = c.rbuf[24+dlen:]
	if len(c.rbuf) == 0 {
		c.rbuf = nil // release the backing array once fully consumed
	}
	return
}

// fill ensures c.rbuf holds at least n bytes, reading from the transport until
// then or until deadline passes. perRead is the timeout handed to each individual
// Read (constant so a timeout-caching transport, e.g. WinUSB pipe policy, isn't
// re-armed every read); deadline bounds the aggregate wait.
func (c *Conn) fill(n int, deadline time.Time, perRead time.Duration) error {
	if c.scratch == nil {
		c.scratch = make([]byte, syncDataMax)
	}
	for len(c.rbuf) < n {
		if !time.Now().Before(deadline) {
			return fmt.Errorf("adb read timed out after %s", perRead)
		}
		m, err := c.t.Read(c.scratch, perRead)
		if err != nil {
			return err
		}
		if m == 0 {
			return io.ErrUnexpectedEOF
		}
		c.rbuf = append(c.rbuf, c.scratch[:m]...)
	}
	return nil
}

// Shell runs command on the device and returns its combined stdout+stderr. A
// non-zero exit yields an *ExitError. Uses shell-protocol-v2 (required by
// Connect), which frames the exit status the engine relies on.
func (c *Conn) Shell(command string) (string, error) {
	local := c.nextID
	c.nextID++
	if err := c.writeMsg(cmdOPEN, local, 0, []byte("shell,v2,raw:"+command+"\x00")); err != nil {
		return "", err
	}

	var out []byte
	var buf []byte // accumulated, undecoded stream bytes
	exit := -1
	for {
		// Decode whole shell-v2 packets out of buf.
		for len(buf) >= 5 {
			length := int(binary.LittleEndian.Uint32(buf[1:5]))
			if len(buf) < 5+length {
				break
			}
			id, payload := buf[0], buf[5:5+length]
			switch id {
			case shellStdout, shellStderr:
				out = append(out, payload...)
			case shellExit:
				if length >= 1 {
					exit = int(payload[0])
				}
			}
			buf = buf[5+length:]
		}
		cmd, a0, a1, data, err := c.readMsg(IOTimeout)
		if err != nil {
			return string(out), err
		}
		if a1 != local {
			continue // stale message addressed to a previous stream
		}
		switch cmd {
		case cmdWRTE:
			buf = append(buf, data...)
			if err := c.writeMsg(cmdOKAY, local, a0, nil); err != nil {
				return string(out), err
			}
		case cmdCLSE:
			_ = c.writeMsg(cmdCLSE, local, a0, nil)
			switch {
			case exit > 0:
				return string(out), &ExitError{Code: exit}
			case exit < 0:
				// Stream closed without an exit-status packet: abnormal for shell_v2
				// (a killed/OOM'd command, or a truncated stream). Do not report success.
				return string(out), fmt.Errorf("shell stream closed without an exit status")
			default:
				return string(out), nil
			}
		}
	}
}

// Push streams r to remotePath via the sync SEND service with the given unix mode
// (e.g. 0644). It reads in syncDataMax chunks so a multi-GB image (the rootfs) is
// never buffered whole in memory.
func (c *Conn) Push(r io.Reader, remotePath string, mode int) error {
	local := c.nextID
	c.nextID++
	if err := c.writeMsg(cmdOPEN, local, 0, []byte("sync:\x00")); err != nil {
		return err
	}
	remote, err := c.expectOkay(local)
	if err != nil {
		return fmt.Errorf("opening sync stream: %w", err)
	}

	// sendWrite sends one WRTE and waits for the device's OKAY (stream flow
	// control). A sync FAIL status can arrive as a WRTE before that OKAY (e.g. a
	// bad path in SEND, or the device /tmp filling mid-DATA); inspect it and abort
	// rather than acking and discarding it.
	sendWrite := func(p []byte) error {
		if err := c.writeMsg(cmdWRTE, local, remote, p); err != nil {
			return err
		}
		for {
			cmd, a0, a1, pd, err := c.readMsg(IOTimeout)
			if err != nil {
				return err
			}
			if a1 != local {
				continue // stale message addressed to a previous stream
			}
			switch cmd {
			case cmdOKAY:
				return nil
			case cmdWRTE:
				_ = c.writeMsg(cmdOKAY, local, a0, nil)
				if isSyncFail(pd) {
					return fmt.Errorf("push rejected: %s", syncFailMsg(pd))
				}
				// A non-FAIL WRTE before our OKAY is unexpected mid-stream; keep waiting.
			case cmdCLSE:
				return fmt.Errorf("sync stream closed unexpectedly")
			}
		}
	}

	// SEND "<path>,<mode>"
	if err := sendWrite(syncReq("SEND", []byte(fmt.Sprintf("%s,%d", remotePath, mode)))); err != nil {
		return fmt.Errorf("SEND: %w", err)
	}
	// DATA chunks, streamed from r.
	buf := make([]byte, syncDataMax)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if err := sendWrite(syncReq("DATA", buf[:n])); err != nil {
				return fmt.Errorf("DATA: %w", err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("reading push source: %w", rerr)
		}
	}
	// DONE <mtime>. The device answers with a stream OKAY (flow-control ack of this
	// write) and a WRTE carrying the sync result ("OKAY" or "FAIL"+msg), in either
	// order — wait for the result WRTE specifically, tolerating an OKAY first.
	done := make([]byte, 8)
	copy(done, "DONE")
	binary.LittleEndian.PutUint32(done[4:], 0)
	if err := c.writeMsg(cmdWRTE, local, remote, done); err != nil {
		return fmt.Errorf("DONE: %w", err)
	}
	for {
		cmd, a0, a1, pd, err := c.readMsg(IOTimeout)
		if err != nil {
			return fmt.Errorf("reading sync result: %w", err)
		}
		if a1 != local {
			continue // stale message addressed to a previous stream
		}
		switch cmd {
		case cmdOKAY:
			// Flow-control ack of our DONE; the result WRTE is still coming.
		case cmdWRTE:
			_ = c.writeMsg(cmdOKAY, local, a0, nil)
			if isSyncFail(pd) {
				return fmt.Errorf("push rejected: %s", syncFailMsg(pd))
			}
			_ = c.writeMsg(cmdCLSE, local, remote, nil)
			return nil
		case cmdCLSE:
			return fmt.Errorf("sync stream closed before push result")
		}
	}
}

// isSyncFail reports whether a sync response payload is a "FAIL" status.
func isSyncFail(pd []byte) bool {
	return len(pd) >= 4 && string(pd[0:4]) == "FAIL"
}

// syncFailMsg extracts the human-readable reason from a "FAIL"+len+msg payload.
func syncFailMsg(pd []byte) string {
	if len(pd) > 8 {
		return string(pd[8:])
	}
	return "unknown error"
}

// expectOkay reads until an OKAY for our stream and returns the device's stream id.
// Messages addressed to a different local stream (arg1) are stale (e.g. the CLSE
// ack of a previous stream on this connection) and skipped.
func (c *Conn) expectOkay(local uint32) (uint32, error) {
	for {
		cmd, a0, a1, _, err := c.readMsg(IOTimeout)
		if err != nil {
			return 0, err
		}
		if a1 != local {
			continue
		}
		switch cmd {
		case cmdOKAY:
			return a0, nil
		case cmdCLSE:
			return 0, fmt.Errorf("stream rejected by device")
		}
	}
}

// syncReq builds a sync request: 4-byte id + 4-byte length + payload.
func syncReq(id string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	copy(b[0:], id)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(payload)))
	copy(b[8:], payload)
	return b
}
