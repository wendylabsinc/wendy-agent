//go:build darwin

package discovery

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework CoreBluetooth -framework Foundation
#include "bluetooth_darwin.h"
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"unsafe"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

const (
	wendyBLEServiceUUID = "7565e9eb-4c20-4b67-9272-d708b397b631"
	wendyL2CAPPSM       = 128
)

var (
	bleCheckOnce sync.Once
	bleCheckErr  error
)

// RunBLECheck calls into CoreBluetooth to test whether BLE is available.
// This may SIGABRT in sandboxed terminals — it is meant to be called from
// the __ble-check subprocess, not the main process.
func RunBLECheck() int {
	return int(C.wendy_ble_check())
}

// discoverBluetooth uses CoreBluetooth via CGo to scan for WendyOS BLE
// peripherals on macOS. It scans for devices advertising the Wendy service
// UUID and returns them sorted by RSSI (strongest first).
func discoverBluetooth(ctx context.Context, activeScan bool) ([]models.BluetoothDevice, error) {
	// Run a one-time subprocess check to test CoreBluetooth access.
	// We spawn a subprocess by re-invoking the wendy binary with __ble-check
	// so that the child gets a fresh Obj-C runtime and can safely call
	// CoreBluetooth APIs. If the child is killed (SIGABRT from a sandboxed
	// terminal) or exits non-zero, BLE is unavailable.
	bleCheckOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return // can't locate self, assume BLE is available
		}
		cmd := exec.CommandContext(ctx, exe, "__ble-check")
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			bleCheckErr = fmt.Errorf("Bluetooth unavailable - your terminal may not have Bluetooth permission")
		}
	})
	if bleCheckErr != nil {
		return nil, bleCheckErr
	}

	scanSeconds := 5
	if !activeScan {
		scanSeconds = 3
	}

	return scanBLEWithContext(ctx, scanSeconds)
}

// bleScanFn performs the blocking CoreBluetooth scan for scanSeconds and
// returns the discovered devices, sorted by RSSI descending (strongest
// signal first). Indirected so scanBLEWithContext's cancellation logic can
// be unit tested without cgo — cgo cannot be used from _test.go files, so
// this default implementation has to stay here (see dnssd_darwin.go's
// dnssdRegister for the same constraint).
var bleScanFn = func(scanSeconds int) ([]models.BluetoothDevice, error) {
	result := C.wendy_ble_scan(C.int(scanSeconds))
	defer C.wendy_ble_free_result(result)

	if result.error != nil {
		return nil, fmt.Errorf("%s", C.GoString(result.error))
	}

	if result.count == 0 || result.devices == nil {
		return nil, nil
	}

	count := int(result.count)
	cDevices := unsafe.Slice(result.devices, count)

	devices := make([]models.BluetoothDevice, 0, count)
	for _, cd := range cDevices {
		psm := uint16(wendyL2CAPPSM)
		displayName := C.GoString(cd.name)
		if cd.is_lite != 0 {
			psm = 0
			if displayName == "" {
				displayName = "Wendy Lite"
			}
		}
		devices = append(devices, models.BluetoothDevice{
			ID:            C.GoString(cd.uuid),
			DisplayName:   displayName,
			Address:       C.GoString(cd.uuid),
			RSSI:          int(cd.rssi),
			IsWendyDevice: true,
			L2CAPPSM:      psm,
		})
	}

	// Sort by RSSI descending (strongest signal first).
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].RSSI > devices[j].RSSI
	})

	return devices, nil
}

// bleScanResult carries a bleScanFn outcome across scanBLEWithContext's
// internal channel.
type bleScanResult struct {
	devices []models.BluetoothDevice
	err     error
}

// scanBLEWithContext runs bleScanFn(scanSeconds) but does not wait on it past
// ctx cancellation. bleScanFn wraps wendy_ble_scan, a blocking cgo call with
// no ctx awareness at all: it always runs for the full scanSeconds via
// nanosleep and cannot be interrupted mid-flight from Go once started. This
// bounds the WAIT rather than the call — mirroring SerialDiscovery's
// probeWithWatchdog/WaitForIdle pattern (serial_discovery.go) — so a caller
// whose ctx is cancelled (e.g. the picker's Bluetooth polling goroutine when
// the user quits) is unblocked immediately instead of sitting out the
// remainder of a 3-5s scan. The scan itself keeps running in the background
// goroutine until it naturally finishes; its result is simply discarded if
// nothing is left to receive it.
func scanBLEWithContext(ctx context.Context, scanSeconds int) ([]models.BluetoothDevice, error) {
	// Read the indirection var here, on the caller's goroutine, rather than
	// inside the goroutine below: the `go` statement's happens-before
	// guarantee then ensures this read is ordered before anything the caller
	// does after scanBLEWithContext returns (including a test's deferred
	// restore of bleScanFn) — reading it directly inside the spawned
	// goroutine has no such ordering against a caller that stops waiting on
	// it, and races under -race. Mirrors probeWithWatchdog in
	// serial_discovery.go.
	scan := bleScanFn
	ch := make(chan bleScanResult, 1)
	go func() {
		devices, err := scan(scanSeconds)
		ch <- bleScanResult{devices: devices, err: err}
	}()

	select {
	case r := <-ch:
		return r.devices, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
