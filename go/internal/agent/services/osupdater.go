package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// Backend identifiers. These strings are the contract shared across the agent
// (featureset advertisement, the pending-update marker, and the proto
// updater_backend field), so they must stay stable.
const updaterNameWendyOS = "wendyos-update"

// osUpdater abstracts an OS A/B update backend. The agent selects a
// registered backend via chooseUpdater/chooseUpdaterForCommit — currently
// just the in-house wendyos-update engine — and the post-reboot healthcheck
// gate (oshealth.Gate) is backend-agnostic: it injects whichever backend's
// commit/rollback the pending update recorded.
type osUpdater interface {
	// name reports the backend identifier (updaterNameWendyOS). It is
	// persisted in the pending-update marker so the next boot's gate commits
	// or rolls back with the same backend.
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
	// commit confirms a pending A/B update (exit-2 semantics => UpdaterNothingPending).
	commit() oshealth.UpdaterResult
	// rollback reverts an uncommitted A/B update.
	rollback() oshealth.UpdaterResult
	// delegatesHealthcheck reports whether the backend runs its own post-commit
	// health gate (wendyos-update runs /etc/wendyos-update/health.d inside
	// commit), so the agent's boot-time gate must NOT run its own CheckAll for
	// it. A backend without such a gate (false) keeps the agent gate owning the
	// healthchecks.
	delegatesHealthcheck() bool
	// commitCommand is the binary name surfaced in user-facing commit/rollback
	// failure notes (e.g. "wendyos-update").
	commitCommand() string
}

// productionUpdaters returns the registered backends in preference order for
// the "auto" selection.
func productionUpdaters(logger *zap.Logger) []osUpdater {
	return []osUpdater{
		newWendyOSUpdater(logger),
	}
}

// selectUpdater chooses the backend for an update request. requested is the
// caller's `--updater`/updater_backend value ("", "auto", or
// "wendyos"/"wendyos-update"); artifactURL constrains the choice to the backend
// that can parse the artifact's format.
func selectUpdater(logger *zap.Logger, requested, artifactURL string) (osUpdater, error) {
	return chooseUpdater(requested, artifactURL, productionUpdaters(logger))
}

// requiredUpdaterForArtifact maps an artifact URL to the only backend that can
// install it: .wendy is the wendyos-update format. Returns "" when the
// extension is unknown (no constraint). A device that cannot run the required
// backend is an honest, explained error rather than a silent install that dies
// mid-stream with a parse error.
func requiredUpdaterForArtifact(artifactURL string) string {
	path := artifactURL
	if u, err := url.Parse(artifactURL); err == nil && u.Path != "" {
		path = u.Path
	}
	if strings.HasSuffix(path, ".wendy") {
		return updaterNameWendyOS
	}
	return ""
}

// chooseUpdater applies the selection policy over candidate backends (ordered
// by preference). The artifact's format is authoritative when recognized: only
// the backend that can parse it is eligible, and an unavailable or conflicting
// backend is an immediate, explained error — never a cross-stack fallback.
// Otherwise an explicit backend is honored or fails (no silent fallback), and
// "auto"/"" picks the first candidate that detects the device.
func chooseUpdater(requested, artifactURL string, candidates []osUpdater) (osUpdater, error) {
	required := requiredUpdaterForArtifact(artifactURL)
	switch requested {
	case "", "auto":
		if required != "" {
			return requireUpdaterForFormat(candidates, required)
		}
		for _, u := range candidates {
			if u.detect() {
				return u, nil
			}
		}
		return nil, fmt.Errorf("no OS update backend is available on this device")
	case updaterNameWendyOS, "wendyos":
		if required != "" && required != updaterNameWendyOS {
			return nil, artifactBackendMismatchError(required, requested)
		}
		return requireUpdater(candidates, updaterNameWendyOS)
	default:
		return nil, fmt.Errorf("unknown updater backend %q (expected auto, wendyos, or %s)",
			requested, updaterNameWendyOS)
	}
}

// artifactBackendMismatchError reports an explicit --updater choice that can
// never parse the artifact's format.
func artifactBackendMismatchError(required, requested string) error {
	return fmt.Errorf("the artifact is a %s artifact and cannot be installed with the %q backend", required, requested)
}

// requireUpdaterForFormat returns the backend the artifact format demands, or a
// stack-mismatch explanation when the device cannot run it. A device whose image
// predates the wendyos-update stack hits the migration wall: its partition
// layout is incompatible, so the only way forward is reflashing — say so instead
// of streaming the artifact into a parse error.
func requireUpdaterForFormat(candidates []osUpdater, required string) (osUpdater, error) {
	u, err := requireUpdater(candidates, required)
	if err == nil {
		return u, nil
	}
	switch required {
	case updaterNameWendyOS:
		return nil, errors.New("this OS update uses the wendyos-update stack, which this device does not support " +
			"(its image predates the wendyos-update stack, and the partition layout is incompatible); " +
			"reflash the device with a current WendyOS image to continue receiving OS updates")
	default:
		return nil, err
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
// code 2 is the "nothing pending" signal that `wendyos-update commit` emits.
// `rollback` reports "nothing to roll back" via exit 1, not 2, so for the
// rollback path exit 2 never occurs.
func commitStatusForExitCode(code int) oshealth.UpdaterStatus {
	switch code {
	case 0:
		return oshealth.UpdaterOK
	case 2:
		return oshealth.UpdaterNothingPending
	default:
		return oshealth.UpdaterError
	}
}

// runUpdaterCommit executes "<binary> <subcommand>" (commit or rollback) and
// classifies the result. If the update is never committed, the bootloader
// reverts to the previous slot on the next reboot.
func runUpdaterCommit(logger *zap.Logger, binary, subcommand string) oshealth.UpdaterResult {
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
			return oshealth.UpdaterResult{Status: oshealth.UpdaterError, Output: output, Err: ctx.Err()}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && commitStatusForExitCode(exitErr.ExitCode()) == oshealth.UpdaterNothingPending {
			return oshealth.UpdaterResult{Status: oshealth.UpdaterNothingPending, Output: output}
		}
		logger.Warn("updater command failed",
			zap.String("binary", binary), zap.String("subcommand", subcommand),
			zap.String("output", output), zap.Error(err))
		return oshealth.UpdaterResult{Status: oshealth.UpdaterError, Output: output, Err: err}
	}
	return oshealth.UpdaterResult{Status: oshealth.UpdaterOK, Output: output}
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
