//go:build darwin

package rcm

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/gousb"
)

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

	devs, _ := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
		return d.Vendor == gousb.ID(VendorNVIDIA) && isRecoveryPID(d.Product)
	})
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
	return out, nil
}

// WaitForDeviceAt blocks until the Jetson at pathKey (from ListRecoveryDevices)
// appears in recovery mode, then claims it. An empty pathKey matches any device.
func WaitForDeviceAt(pathKey string) (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		devs, _ := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
			return d.Vendor == gousb.ID(VendorNVIDIA) && isRecoveryPID(d.Product) &&
				(pathKey == "" || portKey(d) == pathKey)
		})
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
