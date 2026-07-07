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

// gateFixture wires a Gate with recorders for every side effect. It defaults
// to the delegated-health path (the real wendyos-update backend, the only
// registered backend): tests that need the agent's own CheckAll instead call
// useAgentHealthchecks.
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
		Logger:          zap.NewNop(),
		StateDir:        fx.dir,
		DelegatedHealth: true,
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

// useAgentHealthchecks switches a fixture's gate to the non-delegated path:
// the agent runs CheckAll against the given fake systemd responses before
// deciding commit vs rollback. Returns the fake so tests can assert call
// counts or mutate sequences mid-test.
func (fx *gateFixture) useAgentHealthchecks(sequences map[string][]map[string]string, services []CriticalService) *fakeSystemctl {
	systemd := &fakeSystemctl{sequences: sequences}
	checker := NewChecker(zap.NewNop())
	checker.PollInterval = 5 * time.Millisecond
	checker.SystemctlShow = systemd.show
	fx.gate.DelegatedHealth = false
	fx.gate.Services = services
	fx.gate.Checker = checker
	return systemd
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

	fx.gate.Run(context.Background())

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

	fx.gate.Run(context.Background())

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

	fx.gate.Run(context.Background())

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

	fx.gate.Run(context.Background())

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

	fx.gate.Run(context.Background())

	if fx.commits != 0 || fx.rollback != 0 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 0/0/0: a never-booted slot must not be committed",
			fx.commits, fx.rollback, fx.reboots)
	}
	if !fx.markerExists(t) {
		t.Error("marker must be left pending for the boot that runs the updated OS")
	}
}

// The following tests exercise the agent-run CheckAll path used when the
// backend does not delegate healthchecking to its own commit.

func TestGateDifferentBootRunsHealthchecks(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("active")},
		"b.service": {loaded("active")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("active")},
		"b.service": {loaded("active")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("active")},
		"b.service": {loaded("active")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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
		UpdaterResult{Status: UpdaterError, Err: errors.New("boom"), Output: "commit exploded"},
		UpdaterResult{})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("active")},
		"b.service": {loaded("active")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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

func TestGateAgentHealthcheckRollbackOK(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{}, UpdaterResult{Status: UpdaterOK, RebootRequired: true})
	systemd := fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("active")},
		"b.service": {{
			"LoadState": "loaded", "ActiveState": "failed", "SubState": "exited",
			"Result": "exit-code", "UnitFileState": "enabled",
		}},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if systemd == nil {
		t.Fatal("expected a fake systemd")
	}
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

func TestGateAgentHealthcheckRollbackNothingPending(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{}, UpdaterResult{Status: UpdaterNothingPending})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("inactive")},
		"b.service": {loaded("inactive")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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

func TestGateAgentHealthcheckRollbackError(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{},
		UpdaterResult{Status: UpdaterError, Err: errors.New("rollback exploded")})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("inactive")},
		"b.service": {loaded("inactive")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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

func TestGateAgentHealthcheckRollbackUnavailable(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{}, UpdaterResult{Status: UpdaterUnavailable})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {loaded("inactive")},
		"b.service": {loaded("inactive")},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.reboots != 0 {
		t.Error("must not reboot when the updater is unavailable")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRollbackFailed {
		t.Fatalf("expected rollback_failed record, got found=%v %+v", found, rec)
	}
}

func TestGateAllSkippedCountsAsHealthy(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterOK}, UpdaterResult{})
	fx.useAgentHealthchecks(map[string][]map[string]string{
		"a.service": {{"LoadState": "not-found", "ActiveState": "inactive"}},
		"b.service": {{"LoadState": "not-found", "ActiveState": "inactive"}},
	}, []CriticalService{
		{Unit: "a.service", Timeout: 50 * time.Millisecond},
		{Unit: "b.service", Timeout: 50 * time.Millisecond},
	})
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

// The following three tests exercise every outcome branch of rollBack() for a
// backend that delegates healthchecking to its own commit (DelegatedHealth,
// the default in this fixture, matching wendyos-update): UpdaterNothingPending
// and UpdaterUnavailable here, UpdaterError (default) in
// TestGateUnhealthyRollbackError, and UpdaterOK in
// TestGateDelegatedCommitRejectedRollsBack. All three trigger the rollback via
// a rejected commit — the updater runs its own health gate
// (/etc/wendyos-update/health.d) inside commit, so there is no separate
// CheckAll failure to trigger it on this path.

func TestGateUnhealthyRollbackNothingPending(t *testing.T) {
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"), Output: "marked failed"},
		UpdaterResult{Status: UpdaterNothingPending})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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

func TestGateUnhealthyRollbackUnavailable(t *testing.T) {
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"), Output: "marked failed"},
		UpdaterResult{Status: UpdaterUnavailable})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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

	fx.gate.Run(context.Background())

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
		t.Errorf("delegated commit must not record agent service results: %+v", rec.Services)
	}
	if rec.FinalizedAt.IsZero() {
		t.Error("committed record should be finalized")
	}
}

func TestGateDelegatedCommitNothingPending(t *testing.T) {
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterNothingPending}, UpdaterResult{})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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
		UpdaterResult{Status: UpdaterOK, RebootRequired: true})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

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

func TestGateDelegatedCommitRejectedRollsBackNoRebootWhenAlreadyOnOrigin(t *testing.T) {
	// The firmware already fell back to the previous slot on its own before
	// this boot even started (e.g. it burned its retry budget). This boot IS
	// the previous slot, so wendyos-update rollback reports reboot_required:
	// false — pure bookkeeping. Rebooting anyway would just cycle an
	// already-healthy boot for no reason.
	fx := newGateFixture(t,
		UpdaterResult{Status: UpdaterError, Err: errors.New("exit status 1"),
			Output: "pending update wendyos-image-... is marked failed; run rollback"},
		UpdaterResult{Status: UpdaterOK, RebootRequired: false})
	fx.writeFreshMarker(t)

	fx.gate.Run(context.Background())

	if fx.commits != 1 || fx.rollback != 1 || fx.reboots != 0 {
		t.Errorf("commits=%d rollback=%d reboots=%d, want 1/1/0 (no reboot needed)",
			fx.commits, fx.rollback, fx.reboots)
	}
	if fx.markerExists(t) {
		t.Error("marker should be cleared once the rollback is finalized")
	}
	rec, found := fx.readResult(t)
	if !found || rec.Outcome != OutcomeRolledBack {
		t.Fatalf("expected rolled_back record, got found=%v %+v", found, rec)
	}
	if rec.FinalizedAt.IsZero() {
		t.Error("rolled_back record should be finalized immediately: no reboot is coming to do it later")
	}
	if rec.FinalOSVersion != "WendyOS-0.11.0" {
		t.Errorf("FinalOSVersion = %q, want the current (already-rolled-back) OS version", rec.FinalOSVersion)
	}
}

func TestGateDelegatedCommitUnavailableNoRollback(t *testing.T) {
	// The recorded backend's binary is gone at commit time. No health verdict
	// was rendered, so the gate must not roll back a real slot — keep the marker
	// for a retry on the next start.
	fx := newGateFixture(t, UpdaterResult{Status: UpdaterUnavailable}, UpdaterResult{Status: UpdaterOK})
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
