package commands

import (
	"fmt"
	"os"
	"time"
)

// portHolder identifies a process holding a serial port open.
type portHolder struct {
	pid     int
	command string // may be empty if the command lookup failed; pid is still shown
}

// findPortHoldersFn looks up which processes hold port open. The default here
// — always report no holder — is the real, permanent behavior on platforms
// with no lookup available (Windows); platforms that can do better repoint
// this var to a real implementation (see espflash_busy_unix.go). A package
// var so tests can stub it without shelling out.
var findPortHoldersFn = findPortHolders

// killProcessFn best-effort kills a process by PID. A package var so tests
// can stub it without touching real processes.
var killProcessFn = func(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// killSettleDelay gives the OS a moment to release the port's file
// descriptor after a kill, before the caller retries. A package var so tests
// can zero it out.
var killSettleDelay = 500 * time.Millisecond

// offerPortBusyRetry runs after a flash attempt failed with ErrPortBusy. It
// reports who (if anyone identifiable) holds port, offers to kill them, and
// reports whether the caller should retry immediately without asking
// again — killing a holder already implies the intent to retry.
func offerPortBusyRetry(port string) (retriedAutomatically bool) {
	holders := findPortHoldersFn(port)
	if len(holders) == 0 {
		fmt.Println("Serial port busy — another program (a serial monitor, " +
			"`wendy device camera view`, or `wendy run`) may still have it open.")
		return false
	}

	fmt.Println("Serial port busy — held by:")
	for _, h := range holders {
		if h.command != "" {
			fmt.Printf("  PID %d  %s\n", h.pid, h.command)
		} else {
			fmt.Printf("  PID %d\n", h.pid)
		}
	}

	question := "Kill this process and retry?"
	if len(holders) > 1 {
		question = "Kill these processes and retry?"
	}
	if !confirmFn(question) {
		return false
	}

	for _, h := range holders {
		killProcessFn(h.pid)
	}
	time.Sleep(killSettleDelay)
	return true
}
