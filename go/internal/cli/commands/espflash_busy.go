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

// findPortHoldersFn looks up which processes hold port open. On darwin and
// linux this is a real lsof/ps lookup (see espflash_busy_unix.go); on Windows
// there is no OS-bundled equivalent, so it permanently reports no holder and
// callers degrade to a generic hint (see espflash_busy_windows.go). A package
// var so tests can stub it without shelling out.
var findPortHoldersFn = findPortHolders

// killProcessFn kills a process by PID, returning why it could not. A package
// var so tests can stub it without touching real processes.
var killProcessFn = func(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// killSettleDelay gives the OS a moment to release the port's file
// descriptor after a kill, before the caller retries. A package var so tests
// can zero it out.
var killSettleDelay = 500 * time.Millisecond

// excludeSelfHolders drops this process and its parent from holders. lsof can
// legitimately name us: a cancelled flash leaves its goroutine holding the
// port's fd until the process exits, and a wrapper shell shows up as the
// parent. Offering to SIGKILL either would kill the running CLI — potentially
// mid-write to a device — so they are never candidates. A list that contained
// nothing else degrades to the same "no holder found" path as an empty one.
func excludeSelfHolders(holders []portHolder) []portHolder {
	self, parent := os.Getpid(), os.Getppid()
	var kept []portHolder
	for _, h := range holders {
		if h.pid == self || h.pid == parent {
			continue
		}
		kept = append(kept, h)
	}
	return kept
}

// offerPortBusyRetry runs after a flash attempt failed with errPortBusy. It
// reports who (if anyone identifiable) holds port, offers to kill them, and
// reports whether the caller should retry immediately without asking
// again — successfully killing a holder already implies the intent to retry.
func offerPortBusyRetry(port string) (retriedAutomatically bool) {
	holders := excludeSelfHolders(findPortHoldersFn(port))
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

	// A kill can fail — most commonly a root-owned holder. Say so instead of
	// silently swallowing it, and only claim an automatic retry if at least
	// one holder actually went away; otherwise the caller falls through to the
	// ordinary retry prompt rather than looping on an unchanged situation.
	killedAny := false
	for _, h := range holders {
		if err := killProcessFn(h.pid); err != nil {
			fmt.Printf("  could not kill PID %d: %v\n", h.pid, err)
			continue
		}
		killedAny = true
	}
	if !killedAny {
		return false
	}
	time.Sleep(killSettleDelay)
	return true
}
