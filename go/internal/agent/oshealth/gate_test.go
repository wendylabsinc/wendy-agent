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
	systemd  *fakeSystemctl
	commits  int
	rollback int
	reboots  int
}

func newGateFixture(t *testing.T, commitResult, rollbackResult MenderResult) *gateFixture {
	t.Helper()
	fx := &gateFixture{
		dir: t.TempDir(),
		systemd: &fakeSystemctl{sequences: map[string][]map[string]string{
			"a.service": {loaded("active")},
			"b.service": {loaded("active")},
		}},
	}
	checker := NewChecker(zap.NewNop())
	checker.PollInterval = 5 * time.Millisecond
	checker.SystemctlShow = fx.systemd.show
	fx.gate = &Gate{
		Logger:   zap.NewNop(),
		StateDir: fx.dir,
		Services: []CriticalService{
			{Unit: "a.service", Timeout: 50 * time.Millisecond},
			{Unit: "b.service", Timeout: 50 * time.Millisecond},
		},
		Checker: checker,
		Commit: func() MenderResult {
			fx.commits++
			return commitResult
		},
		Rollback: func() MenderResult {
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
		ArtifactURL:  "http://example/artifact.mender",
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
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})

	fx.gate.Run(context.Background())

	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1", fx.commits)
	}
	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0", fx.rollback, fx.reboots)
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written on a plain boot")
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("healthchecks ran on a plain boot (%d calls)", n)
	}
}

func TestGateNoMarkerFinalizesRolledBackRecord(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})
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

	fx.gate.Run(context.Background())

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
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})
	finalized := gateNow.Add(-time.Hour)
	seed := UpdateResult{
		Outcome:     OutcomeRolledBack,
		CreatedAt:   gateNow.Add(-2 * time.Hour),
		FinalizedAt: finalized,
	}
	if err := WriteUpdateResult(fx.dir, seed); err != nil {
		t.Fatal(err)
	}

	fx.gate.Run(context.Background())

	rec, _ := fx.readResult(t)
	if !rec.FinalizedAt.Equal(finalized) {
		t.Errorf("FinalizedAt re-stamped: %v, want %v", rec.FinalizedAt, finalized)
	}
}

func TestGateStaleMarker(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{})
	err := WritePendingMarker(fx.dir, PendingMarker{CreatedAt: gateNow.Add(-2 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run(context.Background())

	if fx.markerExists(t) {
		t.Error("stale marker should be cleared")
	}
	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1 (plain commit)", fx.commits)
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("healthchecks should not run for a stale marker (%d calls)", n)
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written for a stale marker")
	}
}

func TestGateCorruptMarker(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})
	if err := os.MkdirAll(fx.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fx.dir, pendingMarkerFile), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	fx.gate.Run(context.Background())

	if fx.markerExists(t) {
		t.Error("corrupt marker should be cleared")
	}
	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1 (plain commit)", fx.commits)
	}
}

func TestGateSameBootLeavesMarkerUntouched(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{Status: MenderOK})
	err := WritePendingMarker(fx.dir, PendingMarker{
		CreatedAt:    gateNow.Add(-2 * time.Minute),
		OldOSVersion: "WendyOS-0.10.4",
		BootID:       "boot-current", // written in this boot: no reboot happened yet
	})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run(context.Background())

	if fx.commits != 0 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 0/0/0 before the reboot",
			fx.commits, fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker must be left pending for the boot that runs the updated OS")
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("healthchecks must not run before the reboot (%d calls)", n)
	}
	if _, found := fx.readResult(t); found {
		t.Error("no result record should be written before the reboot")
	}
}

func TestGateStaleSameBootLeavesMarker(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{Status: MenderOK})
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

	fx.gate.Run(context.Background())

	if fx.commits != 0 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 0/0/0: a never-booted slot must not be committed",
			fx.commits, fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker must be left pending for the boot that runs the updated OS")
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("healthchecks must not run before the reboot (%d calls)", n)
	}
}

func TestGateDifferentBootRunsHealthchecks(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{})
	err := WritePendingMarker(fx.dir, PendingMarker{
		CreatedAt:    gateNow.Add(-2 * time.Minute),
		OldOSVersion: "WendyOS-0.10.4",
		BootID:       "boot-before-update",
	})
	if err != nil {
		t.Fatal(err)
	}

	fx.gate.Run(context.Background())

	if fx.commits != 1 {
		t.Errorf("commits = %d, want 1", fx.commits)
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
}

func TestGateHealthyCommitOK(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 1 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/0/0", fx.commits, fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared after commit")
	}
	rec, found := fx.readResult(t)
	if !found {
		t.Fatal("expected a committed record")
	}
	if rec.Outcome != OutcomeCommitted {
		t.Errorf("Outcome = %q, want committed", rec.Outcome)
	}
	if rec.OldOSVersion != "WendyOS-0.10.4" || rec.NewOSVersion != "WendyOS-0.11.0" {
		t.Errorf("versions: %+v", rec)
	}
	if len(rec.Services) != 2 || rec.Services[0].Status != StatusHealthy {
		t.Errorf("services: %+v", rec.Services)
	}
	if !rec.CreatedAt.Equal(gateNow) || rec.FinalizedAt.IsZero() {
		t.Errorf("timestamps: created=%v finalized=%v", rec.CreatedAt, rec.FinalizedAt)
	}
}

func TestGateHealthyCommitNothingPending(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.markerExists(t) {
		t.Error("marker should be cleared")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
	if rec.Note == "" {
		t.Error("expected a note explaining there was nothing to commit")
	}
}

func TestGateHealthyCommitError(t *testing.T) {
	fx := newGateFixture(t,
		MenderResult{Status: MenderError, Err: errors.New("boom"), Output: "commit exploded"},
		MenderResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if !fx.markerExists(t) {
		t.Error("marker should be kept so the commit is retried next start")
	}
	if fx.reboots != 0 {
		t.Error("must not reboot on commit failure")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitFailed {
		t.Fatalf("expected commit_failed record, got found=%v %+v", found, rec)
	}
	if !strings.Contains(rec.Note, "boom") && !strings.Contains(rec.Note, "commit exploded") {
		t.Errorf("note should carry the commit error, got %q", rec.Note)
	}
}

func TestGateUnhealthyRollbackOK(t *testing.T) {
	fx := newGateFixture(t, MenderResult{}, MenderResult{Status: MenderOK})
	fx.systemd.sequences["b.service"] = []map[string]string{{
		"LoadState": "loaded", "ActiveState": "failed", "SubState": "exited",
		"Result": "exit-code", "UnitFileState": "enabled",
	}}
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 0 {
		t.Error("must not commit when a healthcheck failed")
	}
	if fx.rollback != 1 || fx.reboots != 1 {
		t.Errorf("rollback=%d reboots=%d, want 1/1", fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared before rebooting into the old slot")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back record, got found=%v %+v", found, rec)
	}
	if !rec.FinalizedAt.IsZero() {
		t.Error("rolled_back record must not be finalized until the old slot boots")
	}
	var failed *ServiceResult
	for i := range rec.Services {
		if rec.Services[i].Status == StatusFailed {
			failed = &rec.Services[i]
		}
	}
	if failed == nil || failed.Unit != "b.service" || !strings.Contains(failed.Reason, "timed out") {
		t.Errorf("failure details missing: %+v", rec.Services)
	}
}

func TestGateUnhealthyRollbackNothingPending(t *testing.T) {
	fx := newGateFixture(t, MenderResult{}, MenderResult{Status: MenderNothingPending})
	fx.systemd.sequences["a.service"] = []map[string]string{loaded("inactive")}
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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
	fx := newGateFixture(t, MenderResult{},
		MenderResult{Status: MenderError, Err: errors.New("rollback exploded")})
	fx.systemd.sequences["a.service"] = []map[string]string{loaded("inactive")}
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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

func TestGateUnhealthyMenderUnavailable(t *testing.T) {
	fx := newGateFixture(t, MenderResult{}, MenderResult{Status: MenderUnavailable})
	fx.systemd.sequences["a.service"] = []map[string]string{loaded("inactive")}
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.reboots != 0 {
		t.Error("must not reboot when mender is unavailable")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRollbackFailed {
		t.Fatalf("expected rollback_failed record, got found=%v %+v", found, rec)
	}
}

// delegateHealth flips a fixture's gate to the wendyos-update path: the backend
// runs its own health gate inside commit, so the agent gate skips CheckAll.
func (fx *gateFixture) delegateHealth() {
	fx.gate.DelegatedHealth = true
	fx.gate.UpdaterLabel = "wendyos-update"
}

func TestGateDelegatedHealthyCommitOK(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{})
	fx.delegateHealth()
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 1 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/0/0", fx.commits, fx.rollback, fx.reboots)
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("agent healthchecks must not run for a delegated backend (%d calls)", n)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared after commit")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
	if len(rec.Services) != 0 {
		t.Errorf("delegated commit must not record agent service results: %+v", rec.Services)
	}
	if rec.FinalizedAt.IsZero() {
		t.Error("committed record should be finalized")
	}
}

func TestGateDelegatedCommitNothingPending(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderNothingPending}, MenderResult{})
	fx.delegateHealth()
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("rollback=%d reboots=%d, want 0/0", fx.rollback, fx.reboots)
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("agent healthchecks must not run for a delegated backend (%d calls)", n)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
	if rec.Note == "" {
		t.Error("expected a note explaining there was nothing to commit")
	}
}

func TestGateDelegatedCommitRejectedRollsBack(t *testing.T) {
	// wendyos-update commit ran its health.d, the deployment is marked failed,
	// and commit returned a non-zero exit. The agent rolls back and reboots.
	fx := newGateFixture(t,
		MenderResult{Status: MenderError, Err: errors.New("exit status 1"),
			Output: "pending update wendyos-image-... is marked failed; run rollback"},
		MenderResult{Status: MenderOK})
	fx.delegateHealth()
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 1 || fx.rollback != 1 || fx.reboots != 1 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/1/1", fx.commits, fx.rollback, fx.reboots)
	}
	if n := fx.systemd.callCount("a.service"); n != 0 {
		t.Errorf("agent healthchecks must not run for a delegated backend (%d calls)", n)
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
	fx := newGateFixture(t, MenderResult{Status: MenderUnavailable}, MenderResult{Status: MenderOK})
	fx.delegateHealth()
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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
}

func TestGateDelegatedCommitTimeoutRetries(t *testing.T) {
	// The agent's own commit timeout fired (storage/D-Bus briefly busy early in
	// boot). That is not a health verdict, so keep the marker and retry rather
	// than reverting a possibly-healthy slot.
	fx := newGateFixture(t,
		MenderResult{Status: MenderError, Err: context.DeadlineExceeded, Output: "timed out"},
		MenderResult{Status: MenderOK})
	fx.delegateHealth()
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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

func TestGateAllSkippedCountsAsHealthy(t *testing.T) {
	fx := newGateFixture(t, MenderResult{Status: MenderOK}, MenderResult{})
	fx.systemd.sequences["a.service"] = []map[string]string{{"LoadState": "not-found", "ActiveState": "inactive"}}
	fx.systemd.sequences["b.service"] = []map[string]string{{"LoadState": "not-found", "ActiveState": "inactive"}}
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 1 || fx.rollback != 0 {
		t.Errorf("commits=%d rollback=%d, want 1/0", fx.commits, fx.rollback)
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeCommitted {
		t.Fatalf("expected committed record, got found=%v %+v", found, rec)
	}
}
