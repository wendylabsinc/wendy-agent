package oshealth

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateResultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := UpdateResult{
		Outcome:      OutcomeRolledBack,
		OldOSVersion: "WendyOS-0.10.4",
		NewOSVersion: "WendyOS-0.11.0",
		CreatedAt:    time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Services: []ServiceResult{
			{Unit: "avahi-daemon.service", Status: StatusHealthy},
			{Unit: "containerd.service", Status: StatusFailed, Reason: "timed out after 60s"},
			{Unit: "NetworkManager.service", Status: StatusSkipped, Reason: "unit not present on this device"},
		},
		RollbackError: "",
		Note:          "",
	}
	if err := WriteUpdateResult(dir, r); err != nil {
		t.Fatalf("WriteUpdateResult: %v", err)
	}

	got, found, err := ReadUpdateResult(dir)
	if err != nil {
		t.Fatalf("ReadUpdateResult: %v", err)
	}
	if !found {
		t.Fatal("expected result to be found")
	}
	if got.Outcome != r.Outcome {
		t.Errorf("Outcome = %q, want %q", got.Outcome, r.Outcome)
	}
	if !got.CreatedAt.Equal(r.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, r.CreatedAt)
	}
	if !got.FinalizedAt.IsZero() {
		t.Errorf("FinalizedAt = %v, want zero", got.FinalizedAt)
	}
	if len(got.Services) != 3 {
		t.Fatalf("len(Services) = %d, want 3", len(got.Services))
	}
	if got.Services[1] != r.Services[1] {
		t.Errorf("Services[1] = %+v, want %+v", got.Services[1], r.Services[1])
	}
}

func TestWriteUpdateResultOverwrites(t *testing.T) {
	dir := t.TempDir()
	if err := WriteUpdateResult(dir, UpdateResult{Outcome: OutcomeRolledBack, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	updated := UpdateResult{Outcome: OutcomeRollbackFailed, CreatedAt: time.Now(), RollbackError: "nothing to roll back"}
	if err := WriteUpdateResult(dir, updated); err != nil {
		t.Fatal(err)
	}

	got, found, err := ReadUpdateResult(dir)
	if err != nil || !found {
		t.Fatalf("ReadUpdateResult: found=%v err=%v", found, err)
	}
	if got.Outcome != OutcomeRollbackFailed || got.RollbackError != "nothing to roll back" {
		t.Errorf("got %+v, want overwritten record", got)
	}
}

func TestReadUpdateResultMissing(t *testing.T) {
	_, found, err := ReadUpdateResult(t.TempDir())
	if err != nil {
		t.Fatalf("ReadUpdateResult on empty dir: %v", err)
	}
	if found {
		t.Fatal("expected found=false for missing result")
	}
}

func TestReadUpdateResultCorrupt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, resultFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, found, err := ReadUpdateResult(dir)
	if err == nil {
		t.Fatal("expected error for corrupt result")
	}
	if found {
		t.Fatal("expected found=false for corrupt result")
	}
}
