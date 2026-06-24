//go:build darwin || linux

// USB device handling for the RCM stage (bootROM level).
// USB transfer mechanics translated from NVIDIA tegrarcm usb.c
// (BSD 3-Clause License, Copyright (c) 2011-2016 NVIDIA CORPORATION)
package rcm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/gousb"
)

type uidResult struct {
	data []byte
	err  error
}

// Device represents a Jetson in RCM mode.
type Device struct {
	ctx    *gousb.Context
	dev    *gousb.Device
	iface  *gousb.Interface
	in     *gousb.InEndpoint
	out    *gousb.OutEndpoint
	doneFn func()
	uidCh  <-chan uidResult
}

// WaitForDevice blocks until a supported Jetson appears in RCM mode (up to 60 s).
// Supported PIDs: T234 (Orin, 0x7023) and T264 (AGX Thor, 0x7026).
func WaitForDevice() (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0) // suppress libusb noise (LIBUSB_ERROR_INTERRUPTED, etc.)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, pid := range []gousb.ID{ProductOrin, ProductThor} {
			dev, err := ctx.OpenDeviceWithVIDPID(VendorNVIDIA, pid)
			if err == nil && dev != nil {
				d, err := openDevice(ctx, dev)
				if err != nil {
					dev.Close()
					return nil, err
				}
				return d, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	ctx.Close()
	return nil, fmt.Errorf("timed out waiting for Jetson in recovery mode")
}

// WaitForNv3p waits for the device to re-enumerate after loading the applet.
// The applet may change the USB PID; we look for any NVIDIA device.
func WaitForNv3p() (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0) // suppress libusb noise

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		devs, err := ctx.OpenDevices(func(desc *gousb.DeviceDesc) bool {
			return desc.Vendor == gousb.ID(VendorNVIDIA)
		})
		if err == nil {
			for _, dev := range devs {
				d, err := openDevice(ctx, dev)
				if err == nil {
					return d, nil
				}
				dev.Close()
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	ctx.Close()
	return nil, fmt.Errorf("timed out waiting for nv3p device")
}

func openDevice(ctx *gousb.Context, dev *gousb.Device) (*Device, error) {
	cfg, err := dev.Config(1)
	if err != nil {
		return nil, fmt.Errorf("claiming config: %w", err)
	}

	iface, done, err := dev.DefaultInterface()
	if err != nil {
		cfg.Close()
		return nil, fmt.Errorf("claiming interface: %w", err)
	}

	// Find bulk IN and OUT endpoints
	var inEP *gousb.InEndpoint
	var outEP *gousb.OutEndpoint

	ifaceDesc := iface.Setting
	for _, ep := range ifaceDesc.Endpoints {
		if ep.TransferType != gousb.TransferTypeBulk {
			continue
		}
		if ep.Direction == gousb.EndpointDirectionIn && inEP == nil {
			inEP, err = iface.InEndpoint(int(ep.Number))
			if err != nil {
				done()
				return nil, fmt.Errorf("opening IN endpoint: %w", err)
			}
		} else if ep.Direction == gousb.EndpointDirectionOut && outEP == nil {
			outEP, err = iface.OutEndpoint(int(ep.Number))
			if err != nil {
				done()
				return nil, fmt.Errorf("opening OUT endpoint: %w", err)
			}
		}
	}

	if inEP == nil || outEP == nil {
		done()
		return nil, fmt.Errorf("device missing bulk IN or OUT endpoints")
	}

	// Submit the UID read transfer immediately after endpoint setup. The T234
	// bootROM sends the UID right when the interface is claimed; submitting here
	// (before returning to the caller) maximises the capture window on macOS,
	// where IOKit drops bulk IN data if no transfer is pending.
	ch := make(chan uidResult, 1)
	go func() {
		buf := make([]byte, 16)
		rctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		n, rerr := inEP.ReadContext(rctx, buf)
		if rerr != nil {
			ch <- uidResult{err: rerr}
		} else {
			ch <- uidResult{data: buf[:n]}
		}
		close(ch)
	}()

	return &Device{
		ctx:    ctx,
		dev:    dev,
		iface:  iface,
		in:     inEP,
		out:    outEP,
		doneFn: done,
		uidCh:  ch,
	}, nil
}

func (d *Device) String() string {
	desc := d.dev.Desc
	return fmt.Sprintf("NVIDIA 0x%04x:0x%04x", uint16(desc.Vendor), uint16(desc.Product))
}

func (d *Device) Close() {
	if d.doneFn != nil {
		d.doneFn()
	}
	d.dev.Close()
	d.ctx.Close()
}

// Read reads from the bulk IN endpoint.
func (d *Device) Read(buf []byte) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return d.in.ReadContext(ctx, buf)
}

// Write writes to the bulk OUT endpoint.
func (d *Device) Write(buf []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := d.out.WriteContext(ctx, buf)
	return err
}

// ReadUID returns the unique ID sent by the Orin bootROM on first connect.
// The read is pre-submitted in openDevice to avoid missing the UID on macOS.
func (d *Device) ReadUID() ([]byte, error) {
	result, ok := <-d.uidCh
	if !ok {
		return nil, fmt.Errorf("UID channel closed")
	}
	if result.err != nil {
		return nil, result.err
	}
	return result.data, nil
}

// LoadApplet sends the RCM message containing the applet to the device.
// The device verifies (in open mode: always passes) and executes the applet.
// After this call the device re-enumerates; use WaitForNv3p to reconnect.
func (d *Device) LoadApplet(applet []byte) error {
	msg, err := BuildDLMiniloader(applet, [48]byte{})
	if err != nil {
		return fmt.Errorf("building RCM message: %w", err)
	}

	if err := d.Write(msg); err != nil {
		return fmt.Errorf("sending RCM message: %w", err)
	}

	// Read back status word (4 bytes)
	status := make([]byte, 4)
	if _, err := d.Read(status); err != nil {
		// Device may reset before sending status — treat read error as success
		// TODO: verify T234 status response format on real hardware
		return nil
	}
	_ = status
	return nil
}

// ProductID returns the USB product ID of the connected device.
func (d *Device) ProductID() gousb.ID {
	return d.dev.Desc.Product
}

// IsT264 reports whether the device is a T264 (Thor) chip.
func (d *Device) IsT264() bool {
	return d.ProductID() == ProductThor
}

// ControlRead reads USB string descriptor index 3 from the bootROM via endpoint 0
// (GET_DESCRIPTOR: bmRequestType=0x80, bRequest=0x06, wValue=0x0303, wIndex=0x0000).
// The T23x bootROM returns the chip BR_CID here (see ReadChipID). buf should be at
// least 4 bytes; 96 is typical. Confirmed to work over macOS IOKit on a live T264.
func (d *Device) ControlRead(buf []byte) (int, error) {
	return d.dev.Control(
		0x80,   // rType: IN, standard, device
		0x06,   // request: GET_DESCRIPTOR
		0x0303, // val: STRING descriptor type (0x03), index 3
		0x0000, // idx: language 0
		buf,
	)
}

// ReadChipID reads the chip BR_CID from USB string descriptor index 3. The T23x
// bootROM stores the BR_CID hex string with its characters reversed; this returns
// it un-reversed, e.g. descriptor "0C08FF61…1008" → "80012641…80C0". The ECID is
// the BR_CID with the leading chip/SKU identifier removed.
//
// This goes over endpoint 0 and is the reliable way to read the chip ID on macOS,
// where IOKit drops the bootROM's bulk-IN UID transfer (see ReadUID).
func (d *Device) ReadChipID() (string, error) {
	buf := make([]byte, 96)
	n, err := d.ControlRead(buf)
	if err != nil {
		return "", fmt.Errorf("reading chip-id descriptor: %w", err)
	}
	return parseChipIDDescriptor(buf, n)
}

// parseChipIDDescriptor extracts the BR_CID from a GET_STRING_DESCRIPTOR (index 3)
// response. Layout: buf[0]=bLength, buf[1]=0x03, buf[2:]=UTF-16LE payload. NVIDIA's
// T23x bootROM stores the BR_CID hex string reversed, so we take the low byte of each
// UTF-16LE code unit and reverse the result. Confirmed on a live T264: the reversed
// descriptor equals the BR_CID reported by tegrarcm_v2 --uid.
func parseChipIDDescriptor(buf []byte, n int) (string, error) {
	if n < 4 {
		return "", fmt.Errorf("chip-id descriptor too short: got %d bytes, need at least 4", n)
	}
	length := int(buf[0])
	if length > n {
		length = n
	}
	var sb strings.Builder
	for i := 2; i+1 < length; i += 2 {
		c := buf[i]
		if !isHexDigit(c) {
			return "", fmt.Errorf("chip-id descriptor byte 0x%02x is not a hex digit", c)
		}
		sb.WriteByte(c)
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("chip-id descriptor empty")
	}
	return reverseASCII(sb.String()), nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')
}

func reverseASCII(s string) string {
	b := []byte(s)
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
