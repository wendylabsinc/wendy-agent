package jobops

import (
	"context"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// SubmitObserved optionally waits for terminal evidence without model polling.
// A deadline never cancels the detached job. An observation failure preserves
// the confirmed receipt so a caller can inspect/retry the same named job.
func SubmitObserved(ctx context.Context, conn *grpcclient.AgentConnection, spec Spec, waitSeconds int) (map[string]any, error) {
	if waitSeconds < 0 || waitSeconds > 30 {
		return nil, fmt.Errorf("wait_seconds must be 0..30")
	}
	receipt, err := Submit(ctx, conn, spec)
	if err != nil || waitSeconds == 0 {
		return receipt, err
	}
	jobPath, ok := receipt["job_path"].(string)
	if !ok || jobPath == "" {
		receipt["observation_error"] = "confirmed response did not contain a job path; inspect the named job before retrying"
		receipt["completion"] = "unknown"
		return receipt, nil
	}
	observation, err := Inspect(ctx, conn, jobPath, InspectOptions{WaitSeconds: waitSeconds, UntilTerminal: true})
	if err != nil {
		receipt["observation_error"] = err.Error()
		receipt["completion"] = "unknown"
		return receipt, nil
	}
	receipt["observation"] = observation
	return receipt, nil
}
