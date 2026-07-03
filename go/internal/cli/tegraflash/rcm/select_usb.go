//go:build darwin || linux

package rcm

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/gousb"
)

// ErrUSBAccess reports that a Jetson in recovery mode is present but the OS
// refused to open it (LIBUSB_ERROR_ACCESS). On Linux this means the current user
// lacks permission on the /dev/bus/usb node — fixed by a udev rule or sudo; the
// caller turns this into actionable guidance.
var ErrUSBAccess = errors.New("USB device access denied")

func isRecoveryPID(p gousb.ID) bool {
	return p == gousb.ID(ProductOrin) || p == gousb.ID(ProductThor)
}

// portKey is the stable physical-location key (bus + parent-port chain).
func portKey(desc *gousb.DeviceDesc) string {
	parts := make([]string, len(desc.Path))
	for i, p := range desc.Path {
		parts[i] = strconv.Itoa(p)
	}
	return fmt.Sprintf("%d-%s", desc.Bus, strings.Join(parts, "."))
}

// ListRecoveryDevices enumerates every Jetson currently in USB recovery mode,
// reading each one's chip ECID over EP0 (no interface is claimed).
func ListRecoveryDevices() ([]RecoveryDevice, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)
	defer ctx.Close()

	devs, err := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
		return d.Vendor == gousb.ID(VendorNVIDIA) && isRecoveryPID(d.Product)
	})
	// The filter only opens Jetson recovery devices, so an access error means one
	// is attached but unopenable. Return it alongside whatever did open — a
	// multi-device host must not silently flash the wrong board because the
	// intended one was dropped. Other enumeration errors keep the lenient
	// "rescan" behavior.
	var accessErr error
	if errors.Is(err, gousb.ErrorAccess) {
		accessErr = fmt.Errorf("%w: a Jetson in recovery mode is connected but could not be opened: %v", ErrUSBAccess, err)
	}
	var out []RecoveryDevice
	for _, dev := range devs {
		rd := RecoveryDevice{PathKey: portKey(dev.Desc), Product: uint16(dev.Desc.Product)}
		buf := make([]byte, 96)
		if n, err := dev.Control(0x80, 0x06, 0x0303, 0x0000, buf); err == nil {
			if id, err := parseChipIDDescriptor(buf, n); err == nil {
				rd.ECID = id
			}
		}
		out = append(out, rd)
		dev.Close()
	}
	return out, accessErr
}

// WaitForDeviceAt blocks until the Jetson at pathKey (from ListRecoveryDevices)
// appears in recovery mode, then claims it. An empty pathKey matches any device.
func WaitForDeviceAt(pathKey string) (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		devs, err := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
			return d.Vendor == gousb.ID(VendorNVIDIA) && isRecoveryPID(d.Product) &&
				(pathKey == "" || portKey(d) == pathKey)
		})
		// Access denied won't heal within the wait window; fail now with the
		// classified error instead of spinning to a misleading timeout.
		if len(devs) == 0 && errors.Is(err, gousb.ErrorAccess) {
			ctx.Close()
			return nil, fmt.Errorf("%w: %v", ErrUSBAccess, err)
		}
		var chosen *gousb.Device
		for i, dev := range devs {
			if i == 0 {
				chosen = dev
			} else {
				dev.Close()
			}
		}
		if chosen != nil {
			d, err := openDevice(ctx, chosen)
			if err != nil {
				chosen.Close()
				ctx.Close()
				return nil, err
			}
			return d, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	ctx.Close()
	return nil, fmt.Errorf("timed out waiting for Jetson at usb %s in recovery mode", pathKey)
}
