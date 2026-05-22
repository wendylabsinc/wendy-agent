//go:build linux

package main

import (
	"fmt"

	"github.com/google/gousb"
)

func libusb_clear_halt(_ *gousb.Device, epAddr uint8) error {
	return fmt.Errorf("libusb_clear_halt(0x%02x): not implemented on linux (use -p2-clear-halt instead)", epAddr)
}

func libusb_set_interface_alt(_ *gousb.Device, iface, alt int) error {
	return fmt.Errorf("libusb_set_interface_alt_setting(%d,%d): not implemented on linux", iface, alt)
}

func libusb_bulk_write(_ *gousb.Device, ep uint8, _ []byte, _ uint) (int, error) {
	return 0, fmt.Errorf("libusb_bulk_write(ep=0x%02x): not implemented on linux", ep)
}
