package services

import (
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/agent/oshealth"
)

// The commit-failure reason the gate captures in UpdateResult.Note must reach
// the client: with no shell access to the device, the GetOSUpdateStatus
// response is the only channel for diagnosing why a commit failed.
func TestOSUpdateStatusToProtoSurfacesNote(t *testing.T) {
	record := oshealth.UpdateResult{
		Outcome: oshealth.OutcomeCommitFailed,
		Note:    "wendyos-update commit failed: exit status 1 (tegra: ESRT capsule not staged)",
	}

	if got := osUpdateStatusToProtoV1(record).GetNote(); got != record.Note {
		t.Errorf("v1 note = %q, want %q", got, record.Note)
	}
	if got := osUpdateStatusToProtoV2(record).GetNote(); got != record.Note {
		t.Errorf("v2 note = %q, want %q", got, record.Note)
	}
}
