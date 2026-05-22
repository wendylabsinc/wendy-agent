//go:build darwin

package main

/*
#cgo pkg-config: libusb-1.0
#include <libusb.h>

static int goLibusbClearHalt(libusb_device_handle *h, uint8_t ep) {
	return libusb_clear_halt(h, ep);
}

static int goLibusbSetInterfaceAlt(libusb_device_handle *h, int iface, int alt) {
	return libusb_set_interface_alt_setting(h, iface, alt);
}

static int goLibusbBulkWrite(libusb_device_handle *h, uint8_t ep, unsigned char *data, int length, int *transferred, unsigned int timeout_ms) {
	return libusb_bulk_transfer(h, ep, data, length, transferred, timeout_ms);
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"github.com/google/gousb"
)

// libusb_clear_halt calls IOKit ClearPipeStall on the endpoint, which:
//  1. Sends CLEAR_FEATURE(HALT) control transfer to the device
//  2. Resets the HOST-side DATA toggle and pipe state to "open"
//
// This is different from sending a bare CLEAR_FEATURE control transfer, which
// only resets the device side but leaves the host-side IOKit pipe state unchanged.
func libusb_clear_halt(dev *gousb.Device, epAddr uint8) error {
	h := gosbHandle(dev)
	if h == nil {
		return fmt.Errorf("nil device handle")
	}
	ret := C.goLibusbClearHalt(h, C.uint8_t(epAddr))
	if ret != 0 {
		return fmt.Errorf("libusb_clear_halt(0x%02x): error %d", epAddr, ret)
	}
	return nil
}

// libusb_set_interface_alt calls IOKit SetAlternateInterface, which resets all
// endpoint DATA toggles and pipe states to "open" on BOTH host and device sides.
// More thorough than libusb_clear_halt because it resets all pipes at once.
func libusb_set_interface_alt(dev *gousb.Device, iface, alt int) error {
	h := gosbHandle(dev)
	if h == nil {
		return fmt.Errorf("nil device handle")
	}
	ret := C.goLibusbSetInterfaceAlt(h, C.int(iface), C.int(alt))
	if ret != 0 {
		return fmt.Errorf("libusb_set_interface_alt_setting(%d,%d): error %d", iface, alt, ret)
	}
	return nil
}

// libusb_bulk_write calls libusb_bulk_transfer directly with the given timeout.
// Bypasses gousb's transfer abstraction to test whether gousb is computing a
// shorter timeout than requested, or whether macOS XHCI has its own NAK limit.
func libusb_bulk_write(dev *gousb.Device, ep uint8, data []byte, timeoutMs uint) (int, error) {
	h := gosbHandle(dev)
	if h == nil {
		return 0, fmt.Errorf("nil device handle")
	}
	var transferred C.int
	ret := C.goLibusbBulkWrite(h, C.uint8_t(ep), (*C.uchar)(unsafe.Pointer(&data[0])), C.int(len(data)), &transferred, C.uint(timeoutMs))
	n := int(transferred)
	if ret != 0 {
		return n, fmt.Errorf("libusb_bulk_transfer(ep=0x%02x, len=%d): error %d", ep, len(data), ret)
	}
	return n, nil
}

// gosbHandle extracts the raw *C.libusb_device_handle from a gousb.Device.
// gousb.Device.handle (*libusbDevHandle = *C.libusb_device_handle) is the first
// field of the struct, so reading it via unsafe gives us the raw C pointer.
func gosbHandle(dev *gousb.Device) *C.libusb_device_handle {
	// Device layout: first field is handle *libusbDevHandle.
	// libusbDevHandle is defined as: type libusbDevHandle C.libusb_device_handle
	// so *libusbDevHandle has the same memory representation as *C.libusb_device_handle.
	return *(**C.libusb_device_handle)(unsafe.Pointer(dev))
}
