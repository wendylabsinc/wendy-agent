package oshealth

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
)

// UpdaterStatus classifies the result of an OS update backend commit/rollback
// invocation.
type UpdaterStatus int

const (
	UpdaterOK UpdaterStatus = iota
	// UpdaterNothingPending: the update backend exited with code 2, meaning
	// there is no pending update to commit or roll back.
	UpdaterNothingPending
	// UpdaterUnavailable: the update backend binary is not installed.
	UpdaterUnavailable
	UpdaterError
)

// UpdaterResult is the outcome of an OS update backend invocation.
type UpdaterResult struct {
	Status UpdaterStatus
	Output string
	Err    error
}

// Gate decides, at agent startup, whether a pending A/B update is committed or
// rolled back. The update backend (wendyos-update) runs its own health gate
// inside commit (/etc/wendyos-update/health.d), so the gate performs no
// healthchecks itself: it drives commit and acts on the verdict, rolling back
// when the backend rejects it. All side effects are injected so the decision
// logic is testable.
type Gate struct {
	Logger   *zap.Logger
	StateDir string
	// Commit runs `<backend> commit`.
	Commit func() UpdaterResult
	// Rollback runs `<backend> rollback`.
	Rollback func() UpdaterResult
	// UpdaterLabel names the backend binary in user-facing notes
	// (e.g. "wendyos-update"). Empty defaults to "wendyos-update".
	UpdaterLabel string
	// Reboot restarts the device.
	Reboot func() error
	// OSVersion reports the OS version of the currently running slot.
	OSVersion func() string
	// BootID reports the current kernel boot ID; defaults to CurrentBootID.
	BootID func() string
	// Now defaults to time.Now.
	Now func() time.Time
}

// Run executes the commit-or-rollback decision. It blocks until the backend's
// commit (or rollback) returns; it never panics, and all errors are logged.
func (g *Gate) Run(ctx context.Context) {
	marker, found, err := ReadPendingMarker(g.StateDir)
	if err != nil {
		g.Logger.Warn("Pending OS update marker is unreadable, discarding it", zap.Error(err))
		g.clearMarker()
		g.plainCommit()
		return
	}
	if !found {
		g.plainCommit()
		g.finalizeRolledBackRecord()
		return
	}
	if cur := g.bootID(); marker.BootID != "" && cur != "" && cur == marker.BootID {
		// The marker was written in this same boot: the device has not
		// rebooted into the new slot yet (e.g. the agent restarted between
		// install and reboot). Committing now would confirm an image that has
		// never booted, so leave the update pending for the next boot. This is
		// checked before the staleness guard on purpose: an old same-boot
		// marker still means "the reboot never happened", not "already
		// confirmed", so it must not be plain-committed.
		g.Logger.Info("OS update installed but the device has not rebooted yet; leaving the update pending",
			zap.String("boot_id", cur))
		return
	}

	if age := g.now().Sub(marker.CreatedAt); age > MaxPendingMarkerAge {
		// The marker outlived its update — most likely the updated slot ran
		// an agent without healthcheck support, which committed (or rolled
		// back) without consuming the marker. (A same-boot stale marker was
		// already handled above.)
		g.Logger.Warn("Pending OS update marker is stale, discarding it",
			zap.Time("created_at", marker.CreatedAt), zap.Duration("age", age))
		g.clearMarker()
		g.plainCommit()
		return
	}

	record := UpdateResult{
		OldOSVersion: marker.OldOSVersion,
		NewOSVersion: g.OSVersion(),
		CreatedAt:    g.now(),
	}

	g.Logger.Info("Pending OS update detected; delegating healthchecks to the updater's commit",
		zap.String("old_os_version", marker.OldOSVersion))
	g.commit(record)
}

// commit drives the backend's commit for a pending update and interprets the
// verdict. The backend runs its own health gate inside commit (wendyos-update
// runs /etc/wendyos-update/health.d), so a commit the updater *rejected* is
// the health verdict: it rolls back rather than retrying. The two "no verdict
// was rendered" cases — a missing binary and the agent's own commit timeout —
// keep the marker and retry instead, so a transient early-boot hiccup never
// reverts a possibly-healthy slot.
func (g *Gate) commit(record UpdateResult) {
	res := g.Commit()
	switch res.Status {
	case UpdaterOK:
		record.Outcome = OutcomeCommitted
		record.FinalizedAt = g.now()
		g.writeResult(record)
		g.clearMarker()
		g.Logger.Info("Committed OS update (updater-gated healthchecks passed)",
			zap.String("output", res.Output))
	case UpdaterNothingPending:
		// Already committed (e.g. a previous agent start committed but died
		// before clearing the marker). Treat as success.
		record.Outcome = OutcomeCommitted
		record.FinalizedAt = g.now()
		record.Note = g.updaterLabel() + " reported nothing to commit (update was already committed)"
		g.writeResult(record)
		g.clearMarker()
	case UpdaterUnavailable:
		// The backend recorded in the marker is gone (binary removed between
		// install and this boot). No health verdict was rendered, so do NOT
		// roll back a slot over a missing tool. Keep the marker for a retry on
		// the next start.
		record.Outcome = OutcomeCommitFailed
		record.Note = g.updaterLabel() + " binary not found at commit"
		g.writeResult(record)
		g.Logger.Warn("OS update backend unavailable at commit; leaving the update pending for retry",
			zap.String("backend", g.updaterLabel()))
	default:
		if errors.Is(res.Err, context.DeadlineExceeded) {
			// The agent's own commit timeout fired — likely storage/D-Bus was
			// briefly busy early in boot, exactly the unhealthy-boot conditions
			// this gate runs in. That is not a health verdict, so keep the
			// marker and retry next start rather than reverting a healthy slot.
			record.Outcome = OutcomeCommitFailed
			record.Note = updaterFailureReason(g.updaterLabel(), "commit", res)
			g.writeResult(record)
			g.Logger.Error("OS update commit timed out; leaving the update pending for retry",
				zap.String("reason", record.Note))
			return
		}
		// The updater ran and rejected the commit: its health.d failed (or the
		// deployment is otherwise marked failed). Roll back and reboot — retrying
		// a marked-failed deployment is futile and a degraded slot must not stay
		// up. The commit output is the user-facing reason.
		record.Note = updaterFailureReason(g.updaterLabel(), "commit", res)
		g.Logger.Error("Updater rejected the OS update commit, rolling back",
			zap.String("reason", record.Note))
		g.rollBack(record)
	}
}

func (g *Gate) rollBack(record UpdateResult) {
	// Persist the failure details before doing anything else: after the
	// rollback reboot the old slot must find this record, and its own gate
	// pass must not mistake the boot for a fresh update (hence the marker is
	// also cleared now).
	record.Outcome = OutcomeRolledBack
	g.writeResult(record)
	g.clearMarker()

	res := g.Rollback()
	switch res.Status {
	case UpdaterOK:
		g.Logger.Warn("OS update rolled back, rebooting into previous version",
			zap.String("old_os_version", record.OldOSVersion))
		g.reboot()
	case UpdaterNothingPending, UpdaterUnavailable:
		// Nothing to roll back means a reboot would not change slots, so
		// stay up and reachable instead of reboot-looping.
		record.Outcome = OutcomeRollbackFailed
		if res.Status == UpdaterNothingPending {
			record.RollbackError = g.updaterLabel() + " reported nothing to roll back"
		} else {
			record.RollbackError = g.updaterLabel() + " binary not found"
		}
		g.writeResult(record)
		g.Logger.Error("Cannot roll back OS update", zap.String("reason", record.RollbackError))
	default:
		// The rollback command failed, but the update is uncommitted, so the
		// bootloader falls back to the old slot on reboot anyway.
		record.RollbackError = updaterFailureReason(g.updaterLabel(), "rollback", res)
		g.writeResult(record)
		g.Logger.Error("OS update rollback failed, rebooting to let the bootloader revert",
			zap.String("reason", record.RollbackError))
		g.reboot()
	}
}

func (g *Gate) plainCommit() {
	res := g.Commit()
	switch res.Status {
	case UpdaterOK:
		g.Logger.Info("Committed OS update", zap.String("output", res.Output))
	case UpdaterNothingPending:
		g.Logger.Debug("OS update commit: nothing to commit")
	case UpdaterUnavailable:
		// No update backend available — nothing to do.
	default:
		g.Logger.Warn("OS update commit failed",
			zap.String("output", res.Output), zap.Error(res.Err))
	}
}

// finalizeRolledBackRecord stamps a rolled_back record on the first boot of
// the slot the device rolled back to, confirming the rollback took effect.
func (g *Gate) finalizeRolledBackRecord() {
	record, found, err := ReadUpdateResult(g.StateDir)
	if err != nil || !found {
		return
	}
	if record.Outcome != OutcomeRolledBack || !record.FinalizedAt.IsZero() {
		return
	}
	record.FinalizedAt = g.now()
	record.FinalOSVersion = g.OSVersion()
	g.writeResult(record)
	g.Logger.Warn("Previous OS update was rolled back",
		zap.String("running_os_version", record.FinalOSVersion),
		zap.Any("services", record.Services))
}

func (g *Gate) writeResult(record UpdateResult) {
	if err := WriteUpdateResult(g.StateDir, record); err != nil {
		g.Logger.Error("Failed to persist OS update result", zap.Error(err))
	}
}

func (g *Gate) clearMarker() {
	if err := ClearPendingMarker(g.StateDir); err != nil {
		g.Logger.Error("Failed to clear pending OS update marker", zap.Error(err))
	}
}

func (g *Gate) reboot() {
	if err := g.Reboot(); err != nil {
		g.Logger.Error("Failed to reboot after OS update rollback", zap.Error(err))
	}
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *Gate) bootID() string {
	if g.BootID != nil {
		return g.BootID()
	}
	return CurrentBootID()
}

// updaterLabel is the backend binary name used in user-facing notes. It
// defaults to "wendyos-update" so a Gate constructed without UpdaterLabel
// (e.g. the degraded no-backend path) still reads sensibly.
func (g *Gate) updaterLabel() string {
	if g.UpdaterLabel != "" {
		return g.UpdaterLabel
	}
	return "wendyos-update"
}

func updaterFailureReason(label, subcommand string, res UpdaterResult) string {
	switch res.Status {
	case UpdaterUnavailable:
		return label + " binary not found"
	default:
		reason := label + " " + subcommand + " failed"
		if res.Err != nil {
			reason += ": " + res.Err.Error()
		}
		if res.Output != "" {
			reason += " (" + res.Output + ")"
		}
		return reason
	}
}
