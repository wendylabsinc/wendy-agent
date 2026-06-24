//go:build darwin || linux

package commands

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// preAuthElevation pre-authenticates sudo so the password prompt appears
// on the raw terminal before any TUI takes over.
func preAuthElevation() error {
	fmt.Println("You may be prompted for your password (sudo is required).")
	if err := exec.Command("sudo", "-v").Run(); err != nil {
		return fmt.Errorf("sudo authentication failed: %w", err)
	}
	return nil
}

// keepElevationAlive refreshes the sudo timestamp every minute until ctx is
// cancelled. Call after preAuthElevation() to prevent the cached credential
// from expiring during a long-running operation such as a multi-GB download.
func keepElevationAlive(ctx context.Context) {
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				exec.Command("sudo", "-v").Run() //nolint:errcheck
			}
		}
	}()
}

func elevationHint() string {
	return "You may be prompted for your password (sudo is required)."
}
