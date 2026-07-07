//go:build !windows

package commands

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyTerminalResize delivers a value on ch whenever the controlling
// terminal is resized (SIGWINCH). It returns a stop func that detaches the
// handler.
func notifyTerminalResize(ch chan<- os.Signal) (stop func()) {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}
