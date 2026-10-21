package jobops

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// SubmitOptions folds bounded waiting and selected report retrieval into one
// operation. Collection is requested explicitly and retains the mutating host
// tool's authorization requirements.
type SubmitOptions struct {
	WaitSeconds         int
	ArtifactFiles       []string
	ArtifactDestination string
}

func ValidateSubmitOptions(opts SubmitOptions) error {
	if opts.WaitSeconds < 0 || opts.WaitSeconds > 30 {
		return fmt.Errorf("wait_seconds must be 0..30")
	}
	if len(opts.ArtifactFiles) == 0 && opts.ArtifactDestination == "" {
		return nil
	}
	if opts.WaitSeconds == 0 || !filepath.IsAbs(opts.ArtifactDestination) {
		return fmt.Errorf("artifact collection requires wait_seconds > 0 and an absolute local artifact_destination")
	}
	return validateArtifactFiles(opts.ArtifactFiles)
}

// SubmitWorkflow retains the confirmed receipt on any later observation or
// collection error. A running job returns artifacts_pending without local writes.
func SubmitWorkflow(ctx context.Context, conn *grpcclient.AgentConnection, spec Spec, opts SubmitOptions) (map[string]any, error) {
	if err := ValidateSubmitOptions(opts); err != nil {
		return nil, err
	}
	receipt, err := SubmitObserved(ctx, conn, spec, opts.WaitSeconds)
	if err != nil || len(opts.ArtifactFiles) == 0 {
		return receipt, err
	}
	obs, _ := receipt["observation"].(map[string]any)
	state, _ := obs["reported_state"].(string)
	if state != "completed" && state != "failed" && state != "stopped" && state != "cancelled" {
		receipt["artifacts_pending"] = true
		return receipt, nil
	}
	jobPath, _ := receipt["job_path"].(string)
	artifacts, err := Pull(ctx, conn, jobPath, opts.ArtifactDestination, opts.ArtifactFiles)
	if err != nil {
		receipt["artifact_error"] = err.Error()
		return receipt, nil
	}
	receipt["artifacts"] = artifacts
	return receipt, nil
}
