package commands

import "errors"

// Windows has neither the process-group semantics the reaper relies on nor the
// stranded-plugin failure it exists for. Reporting "unsupported" makes
// reapOrphanedBuildx a no-op, leaving the bounded wait and the actionable error
// as the behavior there.
func psOwnProcesses() ([]procInfo, error) {
	return nil, errors.New("process listing is not supported on Windows")
}

func killProcessGroupByPGID(int) error {
	return errors.New("process groups are not supported on Windows")
}
