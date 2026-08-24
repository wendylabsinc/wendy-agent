package commands

import (
	"os"
	"sync"
)

// watchTerminalResize wires terminal-resize notifications (SIGWINCH via
// notifyTerminalResize on Unix; a no-op on Windows) to sendResize, reporting
// the new size from termSize(fd). Callers gate on term.IsTerminal: a
// non-terminal stdin has no real size, so a stray SIGWINCH would report the
// 24x80 fallback and clobber the remote PTY (WDY-2478). The returned stop
// detaches the handler and ends the watcher goroutine; safe to call twice.
func watchTerminalResize(fd int, sendResize func(rows, cols uint32)) (stop func()) {
	winch := make(chan os.Signal, 1)
	stopNotify := notifyTerminalResize(winch)

	go func() {
		for range winch {
			sendResize(termSize(fd))
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			// signal.Stop (inside stopNotify) guarantees no further sends
			// once it returns, so closing here is safe and lets the watcher
			// goroutine above exit instead of leaking.
			stopNotify()
			close(winch)
		})
	}
}
