// Package osnotify sends best-effort desktop notifications. Missing tooling is
// a silent no-op; it never errors and never blocks meaningfully.
package osnotify

import (
	"os/exec"
)

type cmdRunner func(name string, args ...string) error

var (
	runner   cmdRunner = execRunner
	lookPath           = exec.LookPath
)

func execRunner(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	// Detach: don't block the CLI on the notifier.
	go func() { _ = cmd.Wait() }()
	return nil
}

// Notify shows a desktop notification if platform tooling is available.
func Notify(title, body string) {
	notify(title, body) // platform-specific
}
