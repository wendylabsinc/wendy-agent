// Package permission checks whether this process has OS permission to use
// Bluetooth Low Energy, before any scan or connection touches the platform's
// BLE stack for the first time.
package permission

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// CheckArg is the hidden CLI subcommand a caller re-execs itself as to run
// the actual platform probe (scan.RunBLECheck) in a disposable child
// process. cmd/wendy registers the other half of this contract: a hidden
// command named CheckArg that runs the probe and exits with its result.
const CheckArg = "__ble-check"

// once/-Err cache the CheckArg subprocess canary result for the life of the
// process: terminal Bluetooth permission cannot change while wendy is
// running.
var (
	once sync.Once
	err  error
)

// Preflight verifies BLE access is available before any scanner or GATT
// connection touches the platform Bluetooth stack. Touching CoreBluetooth
// for the first time in a process without Bluetooth TCC permission can
// SIGABRT the whole process instead of returning an error; running that
// first touch in a disposable child re-exec'd with CheckArg means only the
// child dies, and this process gets back a clean error instead.
func Preflight(ctx context.Context) error {
	once.Do(func() {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return // can't locate self, assume BLE is available
		}
		cmd := exec.CommandContext(ctx, exe, CheckArg)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if runErr := cmd.Run(); runErr != nil {
			err = fmt.Errorf("Bluetooth unavailable - your terminal may not have Bluetooth permission")
		}
	})
	return err
}
