package services

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// Backend identifiers. These strings are the contract shared across the agent
// (featureset advertisement, the pending-update marker, and the CLI
// `--updater` flag), so they must stay stable.
const (
	updaterNameWendyOS = "wendyos-update"
	updaterNameMender  = "mender"
)

// osUpdater abstracts an OS A/B update backend. The agent drives the in-house
// wendyos-update engine as the primary backend and falls back to mender. Both
// expose the same install/commit/rollback lifecycle, so the post-reboot
// healthcheck gate (oshealth.Gate) is backend-agnostic — it injects whichever
// backend's commit/rollback the pending update recorded.
type osUpdater interface {
	// name reports the backend identifier (updaterNameWendyOS/updaterNameMender).
	// It is persisted in the pending-update marker so the next boot's gate
	// commits or rolls back with the same backend.
	name() string
	// detect reports whether this backend can update the current device: its
	// binary is installed and (for wendyos-update) a connector supports the board.
	// Used to choose a backend at install time.
	detect() bool
	// available reports only that the backend's binary is installed, without the
	// (for wendyos-update) connector probe detect() runs. The post-reboot gate
	// uses this to commit/roll back the backend that already installed an update:
	// a transient connector probe failure must not strand a healthy slot.
	available() bool
	// install applies artifactURL up to "reboot required" (it does not reboot),
	// streaming progress via onProgress(phase, percent). It returns a
	// user-facing error on failure.
	install(ctx context.Context, artifactURL string, onProgress func(phase string, percent int32)) error
	// commit confirms a pending A/B update (exit-2 semantics => MenderNothingPending).
	commit() oshealth.MenderResult
	// rollback reverts an uncommitted A/B update.
	rollback() oshealth.MenderResult
	// delegatesHealthcheck reports whether the backend runs its own post-commit
	// health gate (wendyos-update runs /etc/wendyos-update/health.d inside
	// commit), so the agent's boot-time gate must NOT run its own CheckAll for
	// it. mender has no health.d, so the agent gate keeps owning the healthchecks.
	delegatesHealthcheck() bool
	// commitCommand is the binary name surfaced in user-facing commit/rollback
	// failure notes ("wendyos-update", "mender-update"). It is distinct from
	// name(): mender's backend id is "mender" but its binary is "mender-update".
	commitCommand() string
}

// productionUpdaters returns the available backends in preference order
// (wendyos-update first, mender second) for the "auto" selection.
func productionUpdaters(logger *zap.Logger) []osUpdater {
	return []osUpdater{
		newWendyOSUpdater(logger),
		newMenderUpdater(logger),
	}
}

// selectUpdater chooses the backend for an update request. requested is the
// caller's `--updater` value ("", "auto", "wendyos"/"wendyos-update", or
// "mender").
func selectUpdater(logger *zap.Logger, requested string) (osUpdater, error) {
	return chooseUpdater(requested, productionUpdaters(logger))
}

// chooseUpdater applies the selection policy over candidate backends (ordered
// by preference). An explicit backend is honored or fails — it never silently
// falls back. "auto"/"" picks the first candidate that detects the device.
func chooseUpdater(requested string, candidates []osUpdater) (osUpdater, error) {
	switch requested {
	case "", "auto":
		for _, u := range candidates {
			if u.detect() {
				return u, nil
			}
		}
		return nil, fmt.Errorf("no OS update backend is available on this device")
	case updaterNameMender:
		return requireUpdater(candidates, updaterNameMender)
	case updaterNameWendyOS, "wendyos":
		return requireUpdater(candidates, updaterNameWendyOS)
	default:
		return nil, fmt.Errorf("unknown updater backend %q (expected auto, %s, or %s)",
			requested, updaterNameWendyOS, updaterNameMender)
	}
}

// requireUpdater returns the named backend only if it is present and detects
// the device; otherwise it errors rather than falling back to another backend.
func requireUpdater(candidates []osUpdater, name string) (osUpdater, error) {
	for _, u := range candidates {
		if u.name() == name {
			if u.detect() {
				return u, nil
			}
			return nil, fmt.Errorf("updater backend %q is not available on this device", name)
		}
	}
	return nil, fmt.Errorf("updater backend %q is not registered", name)
}

// chooseUpdaterForCommit selects the backend the boot-time gate uses to commit
// or roll back a pending update. Unlike chooseUpdater (install-time selection),
// it does NOT run the connector probe (detect): the update was already
// installed by `requested`, so commit must be as reliable as the backend's own
// binary check — gating it behind detect() could leave a healthy slot
// uncommitted on a transient probe failure, which the bootloader then reverts.
// A named backend is returned unconditionally (its commit/rollback report
// "unavailable" if the binary is truly gone); "auto"/"" picks the first backend
// whose binary is present. Returns nil when nothing is usable.
func chooseUpdaterForCommit(requested string, candidates []osUpdater) osUpdater {
	switch requested {
	case updaterNameMender:
		return findUpdater(candidates, updaterNameMender)
	case updaterNameWendyOS, "wendyos":
		return findUpdater(candidates, updaterNameWendyOS)
	case "", "auto":
		for _, u := range candidates {
			if u.available() {
				return u
			}
		}
		return nil
	default:
		return nil
	}
}

// findUpdater returns the candidate with the given name, or nil if unregistered.
func findUpdater(candidates []osUpdater, name string) osUpdater {
	for _, u := range candidates {
		if u.name() == name {
			return u
		}
	}
	return nil
}

// updaterCommitTimeout bounds a single commit/rollback. These are fast
// bootloader-metadata operations; the timeout exists only so a hung backend
// (the likely failure mode exactly when systemd/D-Bus/storage is unhealthy
// early in boot) cannot block agent startup — and therefore the
// commit-or-rollback decision — indefinitely.
const updaterCommitTimeout = 60 * time.Second

// commitStatusForExitCode maps a commit/rollback exit code to a status. Exit
// code 2 is the "nothing pending" signal that `commit` emits (both
// wendyos-update and mender-update mirror this). `rollback` reports "nothing to
// roll back" via exit 1, not 2, so for the rollback path exit 2 never occurs.
func commitStatusForExitCode(code int) oshealth.MenderStatus {
	switch code {
	case 0:
		return oshealth.MenderOK
	case 2:
		return oshealth.MenderNothingPending
	default:
		return oshealth.MenderError
	}
}

// runUpdaterCommit executes "<binary> <subcommand>" (commit or rollback) and
// classifies the result. If the update is never committed, the bootloader
// reverts to the previous slot on the next reboot.
func runUpdaterCommit(logger *zap.Logger, binary, subcommand string) oshealth.MenderResult {
	ctx, cancel := context.WithTimeout(context.Background(), updaterCommitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, subcommand)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			logger.Error("updater command timed out",
				zap.String("binary", binary), zap.String("subcommand", subcommand),
				zap.Duration("timeout", updaterCommitTimeout), zap.String("output", output))
			return oshealth.MenderResult{Status: oshealth.MenderError, Output: output, Err: ctx.Err()}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && commitStatusForExitCode(exitErr.ExitCode()) == oshealth.MenderNothingPending {
			return oshealth.MenderResult{Status: oshealth.MenderNothingPending, Output: output}
		}
		logger.Warn("updater command failed",
			zap.String("binary", binary), zap.String("subcommand", subcommand),
			zap.String("output", output), zap.Error(err))
		return oshealth.MenderResult{Status: oshealth.MenderError, Output: output, Err: err}
	}
	return oshealth.MenderResult{Status: oshealth.MenderOK, Output: output}
}

// exitCodeOf extracts the process exit code from an exec error, or -1 if the
// error is not a process exit (e.g. the binary failed to start).
func exitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
