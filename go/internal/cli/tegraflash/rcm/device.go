//go:build darwin || linux

// USB device handling for the RCM stage (bootROM level).
// USB transfer mechanics translated from NVIDIA tegrarcm usb.c
// (BSD 3-Clause License, Copyright (c) 2011-2016 NVIDIA CORPORATION)
package rcm

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/google/gousb"
)

// usbDebugLevel returns the libusb log level from WENDY_USB_DEBUG (0-4), or 0.
// Level 4 (LIBUSB_LOG_LEVEL_DEBUG) surfaces the darwin backend's IOReturn codes,
// which the generic "transfer failed" gousb error hides.
func usbDebugLevel() int {
	if v := os.Getenv("WENDY_USB_DEBUG"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 4 {
			return n
		}
	}
	return 0
}

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
	ctx.Debug(usbDebugLevel()) // suppress libusb noise (LIBUSB_ERROR_INTERRUPTED, etc.)

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
	ctx.Debug(usbDebugLevel()) // suppress libusb noise

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
	iface, done, err := dev.DefaultInterface()
	if err != nil {
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

	// T234 sends UID immediately on connect; pre-submit a read so IOKit doesn't
	// drop it before we claim the interface. T264 does not send UID at connect
	// time, so skip the pre-read: submitting a bulk IN transfer that will be
	// cancelled leaves the endpoint in a state that blocks subsequent bulk OUT
	// writes on macOS.
	ch := make(chan uidResult, 1)
	if dev.Desc.Product != gousb.ID(ProductThor) {
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
	} else {
		ch <- uidResult{err: fmt.Errorf("T264 does not send UID at connect time")}
		close(ch)
	}

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

// usbBulkChunkSize matches tegrarcm_v2's NvTegraUsbWriteTimeout, which splits
// every bulk OUT into 16 KiB ioctl(USBDEVFS_BULK) chunks. macOS IOKit rejects a
// single bulk transfer of a multi-hundred-KiB RCM image with LIBUSB_TRANSFER_ERROR,
// so we must chunk the same way the reference tool does.
const usbBulkChunkSize = 16 * 1024

// Write writes to the bulk OUT endpoint, splitting into 16 KiB chunks to match
// tegrarcm_v2. If the total length is a multiple of the endpoint max packet size,
// a zero-length packet is sent to signal end-of-transfer, as the reference tool does.
func (d *Device) Write(buf []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for off := 0; off < len(buf); off += usbBulkChunkSize {
		end := off + usbBulkChunkSize
		if end > len(buf) {
			end = len(buf)
		}
		if _, err := d.out.WriteContext(ctx, buf[off:end]); err != nil {
			return err
		}
	}
	// Zero-length packet when the transfer is an exact multiple of the max packet
	// size, so the device knows the transfer is complete.
	mps := d.out.Desc.MaxPacketSize
	if mps > 0 && len(buf) > 0 && len(buf)%mps == 0 {
		if _, err := d.out.WriteContext(ctx, nil); err != nil {
			return err
		}
	}
	return nil
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

// ControlRead reads a USB string descriptor from the bootROM via endpoint 0.
// T23x bootROMs encode RCM state in string descriptor index 3 using a
// GET_DESCRIPTOR control transfer (bmRequestType=0x80, bRequest=0x06,
// wValue=0x0303, wIndex=0x0000). buf must be at least 3 bytes; 96 bytes is typical.
func (d *Device) ControlRead(buf []byte) (int, error) {
	return d.dev.Control(
		0x80,   // rType: IN, standard, device
		0x06,   // request: GET_DESCRIPTOR
		0x0303, // val: STRING descriptor type (0x03), index 3
		0x0000, // idx: language 0
		buf,
	)
}

// RCMState reads the T23x bootROM RCM state via USB control transfer.
// State 0 means the device is freshly reset and ready for image download.
func (d *Device) RCMState() (byte, error) {
	buf := make([]byte, 96)
	n, err := d.ControlRead(buf)
	if err != nil {
		return 0, fmt.Errorf("reading RCM state descriptor: %w", err)
	}
	return parseStateDescriptor(buf, n)
}

// parseStateDescriptor extracts the RCM state byte from a GET_STRING_DESCRIPTOR response.
// NVIDIA's T23x bootROM encodes the state as an ASCII decimal digit in a UTF-16LE string
// descriptor: state 0 → '0' (0x30), state 5 → '5' (0x35), etc. buf[2] is the low byte of
// the first UTF-16LE code unit. Confirmed on live T264 device (buf[2]=0x30 for initial state).
func parseStateDescriptor(buf []byte, n int) (byte, error) {
	if n < 3 {
		return 0, fmt.Errorf("RCM state descriptor too short: got %d bytes, need at least 3", n)
	}
	b := buf[2]
	if b < '0' || b > '9' {
		return 0, fmt.Errorf("RCM state descriptor byte 0x%02x is not an ASCII digit", b)
	}
	return b - '0', nil
}
