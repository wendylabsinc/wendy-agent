//go:build darwin || linux

// USB device handling for the RCM stage (bootROM level).
// USB transfer mechanics translated from NVIDIA tegrarcm usb.c
// (BSD 3-Clause License, Copyright (c) 2011-2016 NVIDIA CORPORATION)
package rcm

import (
	"context"
	"fmt"
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

// WaitForDevice blocks until an Orin or Thor appears in RCM mode (up to 60 s).
func WaitForDevice() (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)

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

	// T234 bootROM sends the UID on bulk IN right at enumeration; pre-submit to
	// avoid missing it on macOS before any transfer is pending.
	// T264 sends nothing on bulk IN at startup (uses nv3p from the start and
	// exposes UID via USB control transfer), so skip this to avoid races.
	ch := make(chan uidResult, 1)
	if dev.Desc.Product != ProductThor {
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
		ch <- uidResult{err: fmt.Errorf("T264 uses control transfer for UID")}
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

// chunkSize matches tegrarcm's USB_SEND_BLOCK_SIZE to avoid macOS bulk-OUT
// transfer failures on large RCM messages.
const chunkSize = 65536

// Write writes to the bulk OUT endpoint in 64KB chunks.
func (d *Device) Write(buf []byte) error {
	for len(buf) > 0 {
		n := len(buf)
		if n > chunkSize {
			n = chunkSize
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := d.out.WriteContext(ctx, buf[:n])
		cancel()
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}

// IsT264 reports whether this device is a Jetson Thor (T264).
func (d *Device) IsT264() bool {
	return d.dev.Desc.Product == ProductThor
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

// ReadUIDT264 reads the T264 bootROM unique ID via USB GET_DESCRIPTOR(STRING, idx=3).
// The T264 bootROM exposes the chip UID as a USB string descriptor rather than
// sending it on the bulk IN endpoint as T234 does.
func (d *Device) ReadUIDT264() ([]byte, error) {
	buf := make([]byte, 256)
	// bmRequestType=0x80 (device→host, standard, device), bRequest=0x06 (GET_DESCRIPTOR)
	// wValue=0x0303 (string descriptor type 0x03, index 3), wIndex=0x0000
	n, err := d.dev.Control(0x80, 0x06, 0x0303, 0x0000, buf)
	if err != nil {
		return nil, err
	}
	if n < 2 {
		return nil, fmt.Errorf("UID descriptor too short: %d bytes", n)
	}
	// USB string descriptor: [bLength][bDescriptorType=0x03][UTF-16LE chars...]
	// Return the raw descriptor bytes (caller decodes as needed).
	return buf[2:n], nil
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
