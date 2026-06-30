package oshealth

import (
	"time"
)

const resultFile = "last-update-result.json"

// Outcome classifies how the most recent OS update attempt ended.
type Outcome string

const (
	// OutcomeCommitted: healthchecks passed and the update was committed.
	OutcomeCommitted Outcome = "committed"
	// OutcomeRolledBack: healthchecks failed and the previous OS was restored.
	OutcomeRolledBack Outcome = "rolled_back"
	// OutcomeRollbackFailed: healthchecks failed but the rollback could not run.
	OutcomeRollbackFailed Outcome = "rollback_failed"
	// OutcomeCommitFailed: healthchecks passed but committing the update failed.
	OutcomeCommitFailed Outcome = "commit_failed"
)

// UpdateResult is the persisted outcome of the most recent OS update attempt.
// It is written by the healthcheck gate on the freshly booted slot and, after
// a rollback, finalized by the old slot once it is running again.
type UpdateResult struct {
	Outcome      Outcome   `json:"outcome"`
	OldOSVersion string    `json:"old_os_version,omitempty"`
	NewOSVersion string    `json:"new_os_version,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	// FinalizedAt is zero until the boot following a rollback confirms the
	// device is back on the previous OS.
	FinalizedAt time.Time `json:"finalized_at,omitzero"`
	// FinalOSVersion is the OS version observed when the record was finalized.
	FinalOSVersion string          `json:"final_os_version,omitempty"`
	Services       []ServiceResult `json:"services,omitempty"`
	RollbackError  string          `json:"rollback_error,omitempty"`
	Note           string          `json:"note,omitempty"`
}

func WriteUpdateResult(dir string, r UpdateResult) error {
	return writeJSONAtomic(dir, resultFile, r)
}

func ClearUpdateResult(dir string) error {
	return removeIfExists(dir, resultFile)
}

func ReadUpdateResult(dir string) (UpdateResult, bool, error) {
	var r UpdateResult
	found, err := readJSON(dir, resultFile, &r)
	if err != nil {
		return UpdateResult{}, false, err
	}
	return r, found, nil
}
