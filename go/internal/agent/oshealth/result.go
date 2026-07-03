package oshealth

import (
	"time"
)

const resultFile = "last-update-result.json"

// Outcome classifies how the most recent OS update attempt ended.
type Outcome string

const (
	// OutcomeCommitted: the updater accepted the commit (its own health
	// verdict, when it renders one) and the update was committed.
	OutcomeCommitted Outcome = "committed"
	// OutcomeRolledBack: the updater rejected the commit (or the update
	// otherwise failed healthchecks) and the previous OS was restored.
	OutcomeRolledBack Outcome = "rolled_back"
	// OutcomeRollbackFailed: the update failed healthchecks but the rollback
	// could not run.
	OutcomeRollbackFailed Outcome = "rollback_failed"
	// OutcomeCommitFailed: no health verdict was rendered — the updater binary
	// was missing at commit, or the agent's own commit timeout fired. The
	// update stays pending and is retried on the next agent start.
	OutcomeCommitFailed Outcome = "commit_failed"
)

// ServiceStatus is the verdict of a single critical-service healthcheck.
//
// A gate only populates ServiceResult when it runs its own CheckAll — a
// backend that delegates healthchecking to its own commit (wendyos-update
// runs /etc/wendyos-update/health.d) renders its verdict through the commit
// result instead, so its UpdateResult.Services is empty.
type ServiceStatus string

const (
	StatusHealthy ServiceStatus = "healthy"
	// StatusSkipped: the unit is not present on this device or is
	// intentionally disabled, so it does not gate the update.
	StatusSkipped ServiceStatus = "skipped"
	StatusFailed  ServiceStatus = "failed"
)

// ServiceResult is the outcome of checking one critical service.
type ServiceResult struct {
	Unit   string        `json:"unit"`
	Status ServiceStatus `json:"status"`
	Reason string        `json:"reason,omitempty"`
}

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
