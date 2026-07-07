//go:build windows

package commands

import "os"

// notifyTerminalResize is a no-op on Windows, which has no SIGWINCH-equivalent
// signal. The attach session still runs; it just won't propagate live terminal
// resizes to the remote PTY.
func notifyTerminalResize(ch chan<- os.Signal) (stop func()) {
	return func() {}
}
