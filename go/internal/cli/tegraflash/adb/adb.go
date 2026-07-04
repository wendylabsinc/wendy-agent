//go:build darwin || linux

// Package adb speaks the Android Debug Bridge wire protocol directly over USB —
// no adb binary and no adb server. It implements the host side of the transport
// (the CNXN handshake plus OPEN/OKAY/WRTE/CLSE stream multiplexing) and the
// "shell:" and "sync:" (file push) services, so the wendy CLI can drive a
// device's adbd self-contained. This is used to drive the T264 initrd-flash
// gadget that comes up after the RCM boot.
//
// The ADB interface is identified by USB vendor class 0xFF / subclass 0x42 /
// protocol 0x01. Auth (the on-device "allow this computer" RSA prompt) is not
// implemented: a headless flashing initrd runs adbd insecure, so the device
// replies to CNXN with CNXN rather than AUTH.
package adb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/gousb"
)

// adbPortKey is the stable physical-location key (bus + parent-port chain), matching
// rcm.portKey, so a device can be tracked across the RCM→ADB re-enumeration.
func adbPortKey(desc *gousb.DeviceDesc) string {
	parts := make([]string, len(desc.Path))
	for i, p := range desc.Path {
		parts[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf("%d-%s", desc.Bus, strings.Join(parts, "."))
}

// ADB message commands (little-endian 4-byte tags).
const (
	cmdCNXN = 0x4e584e43 // "CNXN"
	cmdAUTH = 0x48545541 // "AUTH"
	cmdOPEN = 0x4e45504f // "OPEN"
	cmdOKAY = 0x59414b4f // "OKAY"
	cmdCLSE = 0x45534c43 // "CLSE"
	cmdWRTE = 0x45545257 // "WRTE"

	adbVersion = 0x01000001
	maxPayload = 256 * 1024

	classVendor = 0xff
	subclassADB = 0x42
	protocolADB = 0x01

	syncDataMax = 64 * 1024

	// ioTimeout bounds each USB transfer. It is large because device-side flash
	// steps (QSPI erase, dd of multi-GB partitions) hold the shell stream open for a
	// long time with no output, and a timed-out bulk-IN read is destructive on macOS
	// (it aborts the endpoint), so we must not time out during a legitimate op.
	ioTimeout = 30 * time.Minute

	// handshakeReadTimeout bounds the CNXN reply read specifically. Unlike a data
	// transfer, the handshake reply is immediate on a healthy adbd, so a short
	// timeout here lets Open() fail fast and retry (across bootburn's own
	// `timeout N adb wait-for-device` windows) while the gadget's adbd is still
	// coming up — instead of blocking on the 30-minute ioTimeout until bootburn
	// SIGKILLs the probe (rc 124), which also risks poisoning the USB endpoint.
	handshakeReadTimeout = 3 * time.Second
)

// Device is a connected ADB transport over USB.
type Device struct {
	ctx    *gousb.Context
	dev    *gousb.Device
	cfg    *gousb.Config
	iface  *gousb.Interface
	in     *gousb.InEndpoint
	out    *gousb.OutEndpoint
	nextID uint32
	// Banner is the device identity string from the CNXN reply.
	Banner string
	// shellV2 is set when the device advertises the shell_v2 feature; its legacy
	// shell:/exec: services are non-functional on the T264 flashing adbd, so we must
	// use the shell-protocol-v2 service instead.
	shellV2 bool
}

// shell-protocol-v2 stream message ids (1-byte id + 4-byte LE length + payload).
const (
	shellStdout = 1
	shellStderr = 2
	shellExit   = 3
)

// ExitError reports a non-zero shell exit status.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("shell exited with status %d", e.Code) }

// Open finds a USB device exposing an ADB interface, claims it, and performs the
// CNXN handshake. It retries on transient failures: bootburn drives many short-lived
// adb invocations back-to-back, and macOS/libusb does not release the interface
// synchronously when the previous process exits, so a claim can briefly fail with
// "bad access" until the kernel frees it.
func Open() (*Device, error) {
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		d, err := openOnce()
		if err == nil {
			return d, nil
		}
		lastErr = err
		time.Sleep(time.Duration(150+attempt*150) * time.Millisecond)
	}
	return nil, lastErr
}

func openOnce() (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)

	// Open every device exposing an ADB interface; we select among them below. We do
	// NOT pre-filter by WENDY_ADB_PATH here because the flashing gadget can
	// re-enumerate at a different USB location than the RCM device was selected at.
	wantPath := os.Getenv("WENDY_ADB_PATH")
	devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		_, _, _, ok := findADBInterface(desc)
		return ok
	})
	// OpenDevices returns opened handles plus an aggregate error; proceed as long as
	// we got at least one usable handle.
	if len(devs) == 0 {
		ctx.Close()
		// Distinguish permissions from absence: the gadget re-enumerates with its
		// own PID (0955:7100), so a host whose udev rules cover only the recovery
		// PIDs hits this even after a clean stage 1. The message lands in the
		// flash log, where classifyFlashFailure picks it up.
		if errors.Is(err, gousb.ErrorAccess) {
			return nil, fmt.Errorf("USB access denied opening the flashing gadget: %v (install a udev rule covering USB vendor 0955, or run with sudo)", err)
		}
		return nil, fmt.Errorf("no USB device with an ADB interface (ff/42/01) found: %v", err)
	}

	// Pick which device to drive. WENDY_ADB_PATH pins a physical USB location (bus +
	// parent-port chain) so a multi-device host flashes the chosen board. But the
	// flashing gadget often re-enumerates at a *different* port than the RCM device:
	// on macOS the RCM device is USB-2 Hi-Speed while the ADB gadget is USB-3
	// SuperSpeed, which lands on the companion port (e.g. 1-1 -> 1-2). So when the pin
	// matches nothing but exactly one ADB device is present, fall back to it.
	sel := 0
	if wantPath != "" {
		sel = -1
		for i, d := range devs {
			if adbPortKey(d.Desc) == wantPath {
				sel = i
				break
			}
		}
		if sel == -1 {
			if len(devs) == 1 {
				sel = 0
				fmt.Fprintf(os.Stderr, "wendy adb: no ADB device at usb %s; using the only ADB device present (usb %s)\n", wantPath, adbPortKey(devs[0].Desc))
			} else {
				for _, d := range devs {
					d.Close()
				}
				ctx.Close()
				return nil, fmt.Errorf("no ADB device at usb %s among %d ADB devices present", wantPath, len(devs))
			}
		}
	}
	dev := devs[sel]
	for i, d := range devs {
		if i != sel {
			d.Close()
		}
	}

	// On Linux a kernel driver bound to the interface makes the claim fail with
	// "busy"; auto-detach clears it. No-op on macOS (gousb swallows NOT_SUPPORTED).
	_ = dev.SetAutoDetach(true)

	cfgNum, ifNum, altNum, ok := findADBInterface(dev.Desc)
	if !ok {
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("ADB interface disappeared")
	}

	cfg, err := dev.Config(cfgNum)
	if err != nil {
		// Logged (not just returned) because Open()'s retry loop and bootburn's
		// outer `timeout` otherwise swallow why the claim keeps failing.
		fmt.Fprintf(os.Stderr, "wendy adb: usb %s: claiming config %d failed: %v\n", adbPortKey(dev.Desc), cfgNum, err)
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("claiming config %d: %w", cfgNum, err)
	}
	iface, err := cfg.Interface(ifNum, altNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wendy adb: usb %s: claiming interface %d.%d failed: %v\n", adbPortKey(dev.Desc), ifNum, altNum, err)
		cfg.Close()
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("claiming interface %d.%d: %w", ifNum, altNum, err)
	}

	var inEP *gousb.InEndpoint
	var outEP *gousb.OutEndpoint
	for _, ep := range iface.Setting.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.Direction == gousb.EndpointDirectionIn && inEP == nil {
			inEP, _ = iface.InEndpoint(ep.Number)
		} else if ep.Direction == gousb.EndpointDirectionOut && outEP == nil {
			outEP, _ = iface.OutEndpoint(ep.Number)
		}
	}
	if inEP == nil || outEP == nil {
		iface.Close()
		cfg.Close()
		dev.Close()
		ctx.Close()
		return nil, fmt.Errorf("ADB interface is missing bulk endpoints")
	}

	d := &Device{ctx: ctx, dev: dev, cfg: cfg, iface: iface, in: inEP, out: outEP, nextID: 1}
	if err := d.connect(); err != nil {
		// The ADB interface was found and claimed but the CNXN handshake failed.
		// Surface why here: Open() retries internally and bootburn's outer `timeout`
		// often kills the process mid-retry before Open() returns, so this is the
		// only place the underlying reason (adbd not up, USB read error, auth) is
		// visible in the flash log.
		fmt.Fprintf(os.Stderr, "wendy adb: claimed ADB interface at usb %s but CNXN handshake failed: %v\n", adbPortKey(dev.Desc), err)
		d.Close()
		return nil, err
	}
	return d, nil
}

// VendorPresent reports whether any USB device with the given vendor id is present.
// It only inspects descriptors (the filter returns false), so no device is claimed.
func VendorPresent(vid uint16) bool {
	ctx := gousb.NewContext()
	defer ctx.Close()
	found := false
	devs, _ := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
		if uint16(desc.Vendor) == vid {
			found = true
		}
		return false
	})
	for _, d := range devs {
		d.Close()
	}
	return found
}

// findADBInterface returns the config/interface/alt numbers of the first ADB
// interface (vendor class 0xFF, subclass 0x42, protocol 0x01) in desc.
func findADBInterface(desc *gousb.DeviceDesc) (cfgNum, ifNum, altNum int, ok bool) {
	for _, c := range desc.Configs {
		for _, i := range c.Interfaces {
			for _, a := range i.AltSettings {
				if uint8(a.Class) == classVendor && uint8(a.SubClass) == subclassADB && uint8(a.Protocol) == protocolADB {
					return c.Number, a.Number, a.Alternate, true
				}
			}
		}
	}
	return 0, 0, 0, false
}

// Close releases the USB resources.
func (d *Device) Close() {
	if d.iface != nil {
		d.iface.Close()
	}
	if d.cfg != nil {
		d.cfg.Close()
	}
	if d.dev != nil {
		d.dev.Close()
	}
	if d.ctx != nil {
		d.ctx.Close()
	}
}

func (d *Device) connect() error {
	if err := d.writeMsg(cmdCNXN, adbVersion, maxPayload, []byte("host::\x00")); err != nil {
		return fmt.Errorf("sending CNXN: %w", err)
	}
	// Bound the reply read: a healthy adbd answers immediately, so a slow/absent
	// reply means the gadget isn't ready — fail fast so Open() can retry rather
	// than block on the 30-minute data timeout.
	for {
		cmd, _, _, data, err := d.readMsgTimeout(handshakeReadTimeout)
		if err != nil {
			return fmt.Errorf("reading CNXN reply: %w", err)
		}
		switch cmd {
		case cmdCNXN:
			d.Banner = string(data)
			d.shellV2 = strings.Contains(d.Banner, "shell_v2")
			return nil
		case cmdAUTH:
			return fmt.Errorf("device requires ADB authentication (secure adbd); not supported")
		}
	}
}

// writeMsg sends one ADB message: a 24-byte header then the payload.
func (d *Device) writeMsg(cmd, arg0, arg1 uint32, data []byte) error {
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

	ctx, cancel := context.WithTimeout(context.Background(), ioTimeout)
	defer cancel()
	if _, err := d.out.WriteContext(ctx, hdr); err != nil {
		return err
	}
	if len(data) > 0 {
		if _, err := d.out.WriteContext(ctx, data); err != nil {
			return err
		}
	}
	return nil
}

// readMsg reads one ADB message: a 24-byte header then data_length bytes of payload.
func (d *Device) readMsg() (cmd, arg0, arg1 uint32, data []byte, err error) {
	return d.readMsgTimeout(ioTimeout)
}

// readMsgTimeout is readMsg with an explicit per-transfer timeout, so the CNXN
// handshake can use a short bound while data transfers keep the long ioTimeout.
func (d *Device) readMsgTimeout(timeout time.Duration) (cmd, arg0, arg1 uint32, data []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Read into a full max-packet buffer (a sub-packet read length can error on
	// macOS IOKit). adbd writes the header as its own transfer, so this returns 24.
	hdr := make([]byte, 512)
	n, e := d.in.ReadContext(ctx, hdr)
	if e != nil {
		err = e
		return
	}
	if n < 24 {
		err = fmt.Errorf("short ADB header (%d bytes)", n)
		return
	}
	cmd = binary.LittleEndian.Uint32(hdr[0:])
	arg0 = binary.LittleEndian.Uint32(hdr[4:])
	arg1 = binary.LittleEndian.Uint32(hdr[8:])
	dlen := binary.LittleEndian.Uint32(hdr[12:])
	// The length comes from the device; bound it so a malformed/oversized header
	// can't drive an unbounded allocation in the reassembly loop below. ADB payloads
	// never exceed the negotiated max.
	if dlen > maxPayload {
		err = fmt.Errorf("ADB payload too large: %d bytes (max %d)", dlen, maxPayload)
		return
	}

	got := append([]byte{}, hdr[24:n]...) // any payload that rode with the header
	for uint32(len(got)) < dlen {
		buf := make([]byte, syncDataMax)
		m, re := d.in.ReadContext(ctx, buf)
		if re != nil {
			err = re
			return
		}
		got = append(got, buf[:m]...)
	}
	data = got[:dlen]
	return
}

// Shell runs command on the device and returns its combined stdout+stderr. If the
// command exits non-zero the returned error is an *ExitError carrying the code.
//
// The T264 flashing adbd advertises shell_v2 and does not service the legacy
// "shell:"/"exec:" requests (they return "fork failed"), so when shell_v2 is
// available we use the shell-protocol-v2 service ("shell,v2,raw:") and decode its
// framed stream. Otherwise we fall back to the legacy raw service.
func (d *Device) Shell(command string) (string, error) {
	if d.shellV2 {
		return d.shellV2Run("shell,v2,raw:" + command)
	}
	local := d.nextID
	d.nextID++
	if err := d.writeMsg(cmdOPEN, local, 0, []byte("shell:"+command+"\x00")); err != nil {
		return "", err
	}
	var out []byte
	for {
		cmd, a0, _, data, err := d.readMsg()
		if err != nil {
			return string(out), err
		}
		switch cmd {
		case cmdOKAY:
			// stream is open / our write was accepted
		case cmdWRTE:
			out = append(out, data...)
			if err := d.writeMsg(cmdOKAY, local, a0, nil); err != nil {
				return string(out), err
			}
		case cmdCLSE:
			_ = d.writeMsg(cmdCLSE, local, a0, nil)
			return string(out), nil
		}
	}
}

// shellV2Run opens a shell-protocol-v2 service and decodes its framed stream into
// combined stdout+stderr, returning an *ExitError if the device reports non-zero.
func (d *Device) shellV2Run(service string) (string, error) {
	local := d.nextID
	d.nextID++
	if err := d.writeMsg(cmdOPEN, local, 0, []byte(service+"\x00")); err != nil {
		return "", err
	}

	var out []byte
	var buf []byte // accumulated, undecoded stream bytes
	exit := -1
	for {
		// Decode whole packets out of buf.
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
		cmd, a0, _, data, err := d.readMsg()
		if err != nil {
			return string(out), err
		}
		switch cmd {
		case cmdWRTE:
			buf = append(buf, data...)
			if err := d.writeMsg(cmdOKAY, local, a0, nil); err != nil {
				return string(out), err
			}
		case cmdCLSE:
			_ = d.writeMsg(cmdCLSE, local, a0, nil)
			if exit > 0 {
				return string(out), &ExitError{Code: exit}
			}
			return string(out), nil
		}
	}
}

// Push streams r to remotePath via the sync SEND service with the given unix mode
// (e.g. 0644). It reads in syncDataMax chunks so a multi-GB image (e.g. the rootfs)
// is never buffered whole in memory.
func (d *Device) Push(r io.Reader, remotePath string, mode int) error {
	local := d.nextID
	d.nextID++
	if err := d.writeMsg(cmdOPEN, local, 0, []byte("sync:\x00")); err != nil {
		return err
	}
	remote, err := d.expectOkay(local)
	if err != nil {
		return fmt.Errorf("opening sync stream: %w", err)
	}

	// sendWrite sends one WRTE and waits for the device's OKAY (flow control),
	// acking any WRTE the device sends back in the meantime.
	sendWrite := func(p []byte) error {
		if err := d.writeMsg(cmdWRTE, local, remote, p); err != nil {
			return err
		}
		for {
			cmd, a0, _, _, err := d.readMsg()
			if err != nil {
				return err
			}
			switch cmd {
			case cmdOKAY:
				return nil
			case cmdWRTE:
				_ = d.writeMsg(cmdOKAY, local, a0, nil)
			case cmdCLSE:
				return fmt.Errorf("sync stream closed unexpectedly")
			}
		}
	}

	// SEND "<path>,<mode>"
	pathmode := fmt.Sprintf("%s,%d", remotePath, mode)
	if err := sendWrite(syncReq("SEND", []byte(pathmode))); err != nil {
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
	// DONE <mtime>
	done := make([]byte, 8)
	copy(done, "DONE")
	binary.LittleEndian.PutUint32(done[4:], 0)
	if err := sendWrite(done); err != nil {
		return fmt.Errorf("DONE: %w", err)
	}

	// The device reports the result as a WRTE: "OKAY" or "FAIL"+len+msg.
	cmd, a0, _, pd, err := d.readMsg()
	if err != nil {
		return fmt.Errorf("reading sync result: %w", err)
	}
	if cmd == cmdWRTE {
		_ = d.writeMsg(cmdOKAY, local, a0, nil)
		if len(pd) >= 4 && string(pd[0:4]) == "FAIL" {
			msg := ""
			if len(pd) > 8 {
				msg = string(pd[8:])
			}
			return fmt.Errorf("push rejected: %s", msg)
		}
	}
	_ = d.writeMsg(cmdCLSE, local, remote, nil)
	return nil
}

// Stat returns the unix mode of remotePath via the sync STAT service (0 if the
// path does not exist).
func (d *Device) Stat(remotePath string) (mode uint32, err error) {
	local := d.nextID
	d.nextID++
	if err := d.writeMsg(cmdOPEN, local, 0, []byte("sync:\x00")); err != nil {
		return 0, err
	}
	remote, err := d.expectOkay(local)
	if err != nil {
		return 0, fmt.Errorf("opening sync stream: %w", err)
	}
	if err := d.writeMsg(cmdWRTE, local, remote, syncReq("STAT", []byte(remotePath))); err != nil {
		return 0, err
	}
	for {
		cmd, a0, _, data, err := d.readMsg()
		if err != nil {
			return 0, err
		}
		switch cmd {
		case cmdWRTE:
			_ = d.writeMsg(cmdOKAY, local, a0, nil)
			if len(data) >= 8 && string(data[0:4]) == "STAT" {
				mode = binary.LittleEndian.Uint32(data[4:])
				_ = d.writeMsg(cmdCLSE, local, remote, nil)
				return mode, nil
			}
		case cmdCLSE:
			return mode, nil
		}
	}
}

// IsDir reports whether remotePath is a directory on the device.
func (d *Device) IsDir(remotePath string) bool {
	mode, err := d.Stat(remotePath)
	return err == nil && mode&0o170000 == 0o040000
}

// expectOkay reads until an OKAY for our stream and returns the device's stream id.
// Messages addressed to a different local stream (arg1) are stale (e.g. the CLSE
// ack of a previous stream on this connection) and skipped.
func (d *Device) expectOkay(local uint32) (uint32, error) {
	for {
		cmd, a0, a1, _, err := d.readMsg()
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
