package oshealth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

var gateNow = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

// gateFixture wires a Gate with recorders for every side effect.
type gateFixture struct {
	gate     *Gate
	dir      string
	commits  int
	rollback int
	reboots  int
}

func newGateFixture(t *testing.T, commitResult, rollbackResult UpdaterResult) *gateFixture {
	t.Helper()
	fx := &gateFixture{
		dir: t.TempDir(),
	}
	fx.gate = &Gate{
		Logger:   zap.NewNop(),
		StateDir: fx.dir,
		Commit: func() UpdaterResult {
			fx.commits++
			return commitResult
		},
		Rollback: func() UpdaterResult {
			fx.rollback++
			return rollbackResult
		},
		Reboot: func() error {
			fx.reboots++
			return nil
		},
		OSVersion: func() string { return "WendyOS-0.11.0" },
		BootID:    func() string { return "boot-current" },
		Now:       func() time.Time { return gateNow },
	}
	return fx
}

func (fx *gateFixture) writeFreshMarker(t *testing.T) {
	t.Helper()
	err := WritePendingMarker(fx.dir, PendingMarker{
		CreatedAt:    gateNow.Add(-2 * time.Minute),
		OldOSVersion: "WendyOS-0.10.4",
		ArtifactURL:  "http://example/artifact.wendy",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (fx *gateFixture) markerExists(t *testing.T) bool {
	t.Helper()
	_, found, err := ReadPendingMarker(fx.dir)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func (fx *gateFixture) readResult(t *testing.T) (UpdateResult, bool) {
	t.Helper()
	rec, found, err := ReadUpdateResult(fx.dir)
	if err != nil {
		t.Fatal(err)
	}
	return rec, found
}

func TestGateNoMarkerPlainCommit(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})

	fx.gate.Run()

	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1", fx.commits)
	}
	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0", fx.rollback, fx.reboots)
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written on a plain boot")
	}
}

func TestGateNoMarkerFinalizesRolledBackRecord(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	seed := UpdateResult{
		Outcome:      OutcomeRolledBack,
		OldOSVersion: "WendyOS-0.10.4",
		NewOSVersion: "WendyOS-0.11.0",
		CreatedAt:    gateNow.Add(-5 * time.Minute),
		Services:     []ServiceResult{{Unit: "a.service", Status: StatusFailed, Reason: "timed out"}},
	}
	if err := WriteUpdateResult(fx.dir, seed); err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	rec, found := fx.readResult(t)
	if !found {
		t.Fatal("record disappeared")
	}
	if !rec.FinalizedAt.Equal(gateNow) {
		t.Errorf("FinalizedAt = %v, want %v", rec.FinalizedAt, gateNow)
	}
	if rec.FinalOSVersion != "WendyOS-0.11.0" {
		t.Errorf("FinalOSVersion = %q", rec.FinalOSVersion)
	}
	if rec.Outcome != OutcomeRolledBack || len(rec.Services) != 1 {
		t.Errorf("record content lost: %+v", rec)
	}
}

func TestGateNoMarkerDoesNotRefinalize(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	finalized := gateNow.Add(-time.Hour)
	seed := UpdateResult{
		Outcome:     OutcomeRolledBack,
		CreatedAt:   gateNow.Add(-2 * time.Hour),
		FinalizedAt: finalized,
	}
	if err := WriteUpdateResult(fx.dir, seed); err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	rec, _ := fx.readResult(t)
	if !rec.FinalizedAt.Equal(finalized) {
		t.Errorf("FinalizedAt re-stamped: %v, want %v", rec.FinalizedAt, finalized)
	}
}

func TestGateStaleMarker(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{})
	err := WritePendingMarker(fx.dir, PendingMarker{CreatedAt: gateNow.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	if fx.markerExists(t) {
		t.Error("stale marker should be cleared")
	}
	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1 (plain commit)", fx.commits)
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written for a stale marker")
	}
}

func TestGateCorruptMarker(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	if err := os.MkdirAll(fx.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, pendingMarkerFile), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	if fx.markerExists(t) {
		t.Error("corrupt marker should be cleared")
	}
	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1 (plain commit)", fx.commits)
	}
}

func TestGateSameBootLeavesMarkerUntouched(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{Status: UpdaterOK})
	err := WritePendingMarker(fx.dir, PendingMarker{
		CreatedAt:    gateNow.Add(-2 * time.Minute),
		OldOSVersion: "WendyOS-0.10.4",
		BootID:       "boot-current", // written in this boot: no reboot happened yet
	})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	if fx.commits != 0 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 0/0/0 before the reboot",
			fx.commits, fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker must be left pending for the boot that runs the updated OS")
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written before the reboot")
	}
}

func TestGateStaleSameBootLeavesMarker(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{Status: UpdaterOK})
	// The marker is old enough to look stale, but it was written in the current
	// boot — the device never rebooted into the new slot (e.g. the reboot
	// failed, or a caller that does not reboot left it behind). The same-boot
	// guard must win over the staleness guard: plain-committing here would
	// confirm a slot that has never booted, the exact thing the guard prevents.
	err := WritePendingMarker(fx.dir, PendingMarker{
		CreatedAt: gateNow.Add(-2 * time.Hour),
		BootID:    "boot-current",
	})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run()

	if fx.commits != 0 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 0/0/0: a never-booted slot must not be committed",
			fx.commits, fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker must be left pending for the boot that runs the updated OS")
	}
}

// The following three tests exercise every outcome branch of rollBack():
// UpdaterNothingPending and UpdaterUnavailable here, UpdaterError (default) in
// TestGateUnhealthyRollbackError, and UpdaterOK in
// TestGateDelegatedCommitRejectedRollsBack. All three trigger the rollback via
// a rejected commit, since that is the only way the gate now rolls back — the
// updater runs its own health gate (/etc/wendyos-update/health.d) inside
// commit.

func TestGateUnhealthyRollbackNothingPending(t *testing.T) {
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"), Output: "marked failed"},
		UpdaterResult{Status: UpdaterNothingPending})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.commits != 1 || fx.rollback != 1 {
		t.Errorf("commits=%d rollback=%d, want 1/1", fx.commits, fx.rollback)
	}
	if fx.reboots != 0 {
		t.Error("must not reboot when there is nothing to roll back (no slot change would happen)")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRollbackFailed {
		t.Fatalf("expected rollback_failed record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.RollbackError, "nothing to roll back") {
		t.Errorf("RollbackError = %q", rec.RollbackError)
	}
}

func TestGateUnhealthyRollbackError(t *testing.T) {
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"), Output: "marked failed"},
		UpdaterResult{Status: UpdaterError, Err: errors.New("rollback exploded")})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.reboots != 1 {
		t.Error("should still reboot: the uncommitted update makes the bootloader fall back")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.RollbackError, "rollback exploded") {
		t.Errorf("RollbackError = %q", rec.RollbackError)
	}
}

func TestGateUnhealthyRollbackUnavailable(t *testing.T) {
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"), Output: "marked failed"},
		UpdaterResult{Status: UpdaterUnavailable})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.reboots != 0 {
		t.Error("must not reboot when the updater is unavailable")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRollbackFailed {
		t.Fatalf("expected rollback_failed record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.RollbackError, "wendyos-update binary not found") {
		t.Errorf("RollbackError = %q, want it to name the default wendyos-update label", rec.RollbackError)
	}
}

func TestGateDelegatedHealthyCommitOK(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.commits != 1 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/0/0", fx.commits, fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared after commit")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
	if len(rec.Services) != 0 {
		t.Errorf("commit must not record agent service results: %+v", rec.Services)
	}
	if rec.FinalizedAt.IsZero() {
		t.Error("committed record should be finalized")
	}
}

func TestGateDelegatedCommitNothingPending(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0", fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.Note, "wendyos-update") {
		t.Errorf("note should name the default wendyos-update label, got %q", rec.Note)
	}
}

func TestGateDelegatedCommitRejectedRollsBack(t *testing.T) {
	// wendyos-update commit ran its health.d, the deployment is marked failed,
	// and commit returned a non-zero exit. The agent rolls back and reboots.
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"),
			Output: "pending update wendyos-image-... is marked failed; run rollback"},
		UpdaterResult{Status: UpdaterOK})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.commits != 1 || fx.rollback != 1 || fx.reboots != 1 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/1/1", fx.commits, fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared before rebooting into the old slot")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.Note, "is marked failed") {
		t.Errorf("note should carry the commit output reason, got %q", rec.Note)
	}
	if !rec.FinalizedAt.IsZero() {
		t.Error("rolled_back record must not be finalized until the old slot boots")
	}
}

func TestGateDelegatedCommitUnavailableNoRollback(t *testing.T) {
	// The recorded backend's binary is gone at commit time. No health verdict
	// was rendered, so the gate must not roll back a real slot — keep the marker
	// for a retry on the next start.
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterUnavailable}, UpdaterResult{Status: UpdaterOK})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0 when the backend is unavailable", fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker should be kept so the commit is retried next start")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitFailed {
		t.Fatalf("expected commit_failed record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.Note, "wendyos-update binary not found") {
		t.Errorf("Note = %q, want it to name the default wendyos-update label", rec.Note)
	}
}

func TestGateDelegatedCommitTimeoutRetries(t *testing.T) {
	// The agent's own commit timeout fired (storage/D-Bus briefly busy early in
	// boot). That is not a health verdict, so keep the marker and retry rather
	// than reverting a possibly-healthy slot.
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: context.DeadlineExceeded, Output: "timed out"},
		UpdaterResult{Status: UpdaterOK})
	fx.writeFreshMarker(t)

	fx.gate.Run()

	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0 on a commit timeout", fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker should be kept so the commit is retried next start")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitFailed {
		t.Fatalf("expected commit_failed record, got found=%v %+v", found, rec)
	}
}
