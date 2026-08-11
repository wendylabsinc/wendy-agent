//go:build !windows

package memguard

import (
	"os"
	"syscall"
)

// kill terminates this process immediately with SIGKILL. The signal is
// deliberately uncatchable: a guard that could be swallowed by a signal handler
// or blocked behind a deferred cleanup is not a ceiling. The shell reports it as
// 137, which is a clearer signature in a bug report than a plain nonzero exit.
func kill() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	// Not reached in practice. If the signal somehow was not delivered, exit
	// with the code the shell would have reported for it anyway.
	os.Exit(137)
}
