package services

import (
	"context"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// DeploymentRuntime is optional: runtimes that cannot preserve a previous
// revision must reject verified deployment rather than silently downgrade it.
type DeploymentRuntime interface {
	PrepareDeployment(context.Context, *agentpb.CreateContainerRequest, *appconfig.AppConfig) (DeploymentTransaction, error)
}

// DeploymentRevisionRemover releases retained rollback state after an explicit
// app deletion. Internal replacement/rollback deletion must not call it.
type DeploymentRevisionRemover interface {
	DeleteDeploymentRevisions(context.Context, []string) error
}

// DeploymentTransaction retains the prior container metadata and writable
// snapshot independently of mutable image tags. The caller serializes all
// lifecycle changes for the app and always closes the transaction. Activate
// may fail after cutover, in which case Rollback must still be called.
type DeploymentTransaction interface {
	Revision() string
	PreviousRevision() string
	PreviousWasRunning() bool
	Activate(context.Context) error
	Commit(context.Context) error
	Rollback(context.Context) (<-chan ContainerOutput, error)
	Close(context.Context) error
}

// ContainerReadinessProber checks the actual container on the device. Network
// probes run in its network namespace; exec probes use its runtime identity.
// A nil probe still checks that the container's process is running.
type ContainerReadinessProber interface {
	ProbeReadiness(context.Context, string, *appconfig.ReadinessConfig) error
}
