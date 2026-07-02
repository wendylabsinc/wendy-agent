//go:build linux

package t234

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// usbDevRe matches a USB device sysfs directory name like "1-3" or "2-1.4"
// (bus-port[.port…]), excluding interface dirs (which contain ':').
var usbDevRe = regexp.MustCompile(`^\d+-\d+(\.\d+)*$`)

// ReleaseUSB forces a USB-level disconnect of the flashing gadget so the
// device sees the host let go (its initrd polls the UDC state and proceeds
// only when it leaves "configured"). It unbinds the gadget from the usb
// driver and rebinds it a second later — the same fallback the bundle's own
// initrd-flash script uses when udisks is unavailable. Requires root.
// serial, when set, must match the gadget's USB serial number.
func ReleaseUSB(serial string) error {
	devices, err := filepath.Glob("/sys/bus/usb/devices/*")
	if err != nil {
		return err
	}
	for _, dev := range devices {
		if !usbDevRe.MatchString(filepath.Base(dev)) {
			continue
		}
		if sysfsString(filepath.Join(dev, "idVendor")) != fmt.Sprintf("%04x", GadgetVendorID) ||
			sysfsString(filepath.Join(dev, "idProduct")) != fmt.Sprintf("%04x", GadgetProductID) {
			continue
		}
		if serial != "" && sysfsString(filepath.Join(dev, "serial")) != serial {
			continue
		}
		name := filepath.Base(dev)
		if err := os.WriteFile("/sys/bus/usb/drivers/usb/unbind", []byte(name), 0o200); err != nil {
			return fmt.Errorf("unbinding %s: %w", name, err)
		}
		time.Sleep(time.Second)
		if err := os.WriteFile("/sys/bus/usb/drivers/usb/bind", []byte(name), 0o200); err != nil {
			return fmt.Errorf("rebinding %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("flashing gadget (%04x:%04x) not found on USB", GadgetVendorID, GadgetProductID)
}
