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

func isRecoveryPID(p gousb.ID) bool {
	return IsT234RecoveryPID(uint16(p)) || p == gousb.ID(ProductThor)
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
	if len(devs) > MaxRecoveryDevices {
		for _, dev := range devs {
			dev.Close()
		}
		return nil, fmt.Errorf("refusing recovery discovery with %d devices (maximum %d)", len(devs), MaxRecoveryDevices)
	}
	var out []RecoveryDevice
	for _, dev := range devs {
		rd := describeRecoveryDevice(dev)
		out = append(out, rd)
		dev.Close()
	}
	return out, accessErr
}

func describeRecoveryDevice(dev *gousb.Device) RecoveryDevice {
	rd := RecoveryDevice{PathKey: portKey(dev.Desc), Product: uint16(dev.Desc.Product)}
	buf := make([]byte, 96)
	if n, err := dev.Control(0x80, 0x06, 0x0303, 0x0000, buf); err == nil {
		if id, err := parseChipIDDescriptor(buf, n); err == nil {
			rd.ECID = id
		}
	}
	return rd
}

// WaitForDeviceAt blocks until the Jetson at pathKey (from ListRecoveryDevices)
// appears with expectedProduct, then claims it. An empty pathKey matches any
// physical port; an expectedProduct of zero retains the legacy any-Jetson
// behavior. Recovery installers must always pass the product they selected.
func WaitForDeviceAt(pathKey string, expectedProduct uint16) (*Device, error) {
	return waitForDevice(RecoverySelector{PathKey: pathKey}, expectedProduct, false)
}

// WaitForDevice re-opens and identity-checks the selected recovery device on
// the same handle that will receive the RCM payload. This is the final check
// before the non-persistent recovery handoff; a missing/mismatched ECID never
// falls back to another device at the same port or of the same product family.
func WaitForDevice(selector RecoverySelector, expectedProduct uint16) (*Device, error) {
	normalized, err := NewRecoverySelector(selector.PathKey, selector.ExpectedECIDDigest)
	if err != nil {
		return nil, err
	}
	if normalized.IsZero() {
		return nil, fmt.Errorf("an exact recovery selector is required before the RCM handoff")
	}
	return waitForDevice(normalized, expectedProduct, true)
}

func waitForDevice(selector RecoverySelector, expectedProduct uint16, exact bool) (*Device, error) {
	ctx := gousb.NewContext()
	ctx.Debug(0)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		devs, err := ctx.OpenDevices(func(d *gousb.DeviceDesc) bool {
			// Enumerate and bound the entire recovery set before applying path,
			// product, or ECID filters. A hostile oversized view must not collapse
			// into one apparently safe target after filtering.
			return d.Vendor == gousb.ID(VendorNVIDIA) && isRecoveryPID(d.Product)
		})
		// Access denied won't heal within the wait window; fail now with the
		// classified error instead of spinning to a misleading timeout.
		if errors.Is(err, gousb.ErrorAccess) {
			for _, dev := range devs {
				dev.Close()
			}
			ctx.Close()
			return nil, fmt.Errorf("%w: %v", ErrUSBAccess, err)
		}
		if len(devs) > MaxRecoveryDevices {
			for _, dev := range devs {
				dev.Close()
			}
			ctx.Close()
			return nil, fmt.Errorf("refusing recovery discovery with %d devices (maximum %d)", len(devs), MaxRecoveryDevices)
		}

		candidates := make([]RecoveryDevice, 0, len(devs))
		candidateHandles := make([]*gousb.Device, 0, len(devs))
		productChangedAtPinnedPath := false
		for _, dev := range devs {
			rd := describeRecoveryDevice(dev)
			if expectedProduct != 0 && rd.Product != expectedProduct {
				if selector.PathKey != "" && rd.PathKey == selector.PathKey {
					productChangedAtPinnedPath = true
				}
				continue
			}
			if !exact && selector.PathKey != "" && rd.PathKey != selector.PathKey {
				continue
			}
			candidates = append(candidates, rd)
			candidateHandles = append(candidateHandles, dev)
		}
		if productChangedAtPinnedPath {
			for _, dev := range devs {
				dev.Close()
			}
			ctx.Close()
			return nil, fmt.Errorf("the recovery device at the selected USB path changed product; refusing the RCM handoff")
		}

		var chosen *gousb.Device
		if exact {
			selected, selectErr := SelectRecoveryDevice(candidates, selector)
			if selectErr == nil {
				for i, candidate := range candidates {
					if candidate.PathKey == selected.PathKey && candidate.Product == selected.Product && candidate.ECID == selected.ECID {
						chosen = candidateHandles[i]
						break
					}
				}
			} else if len(candidates) > 0 {
				for _, dev := range devs {
					dev.Close()
				}
				ctx.Close()
				return nil, selectErr
			}
		} else if len(candidates) > 0 {
			// Backward-compatible internal API behavior. Recovery install flows
			// use WaitForDevice with identity + path instead.
			chosen = candidateHandles[0]
		}

		if chosen != nil {
			for _, dev := range devs {
				if dev != chosen {
					dev.Close()
				}
			}
			d, err := openDevice(ctx, chosen)
			if err != nil {
				chosen.Close()
				ctx.Close()
				return nil, err
			}
			return d, nil
		}
		for _, dev := range devs {
			dev.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	ctx.Close()
	return nil, fmt.Errorf("timed out waiting for the selected Jetson product 0x%04x in recovery mode", expectedProduct)
}
