package containerd

import (
	"context"
	"strconv"
	"strings"
	"time"

	cgroupv2 "github.com/containerd/cgroups/v3/cgroup2/stats"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"go.uber.org/zap"
)

// Termination reasons recorded in labelKeyExitReason. Short, stable, and
// machine-readable; the CLI maps them to friendlier text. They answer the
// question a stopped container otherwise can't: *why* did it stop.
const (
	exitReasonExited            = "exited"             // clean exit (code 0)
	exitReasonCrashed           = "crashed"            // non-zero exit code
	exitReasonOOMKilled         = "oom_killed"         // the cgroup OOM killer fired
	exitReasonStartFailed       = "start_failed"       // the task never started (image/OCI error)
	exitReasonEntitlementDenied = "entitlement_denied" // start blocked by a missing/denied entitlement
)

// exitCodeDidNotStart is stored as the exit code when a container failed before
// its task ran, so callers can distinguish "failed to start" from "exited 0".
const exitCodeDidNotStart = -1

// exitDetailMaxLen bounds the stored detail string (a label value).
const exitDetailMaxLen = 512

// classifyExit maps a process exit code to a termination reason. An OOM kill
// overrides a plain non-zero exit, since exit 137 alone can't distinguish an
// out-of-memory kill from any other SIGKILL.
func classifyExit(code uint32, oomKilled bool) string {
	switch {
	case oomKilled:
		return exitReasonOOMKilled
	case code == 0:
		return exitReasonExited
	default:
		return exitReasonCrashed
	}
}

// classifyStartError maps a task create/start failure to a termination reason.
// An entitlement problem is called out explicitly; everything else is a generic
// start failure carrying the error text as detail.
func classifyStartError(err error) string {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "entitlement") {
		return exitReasonEntitlementDenied
	}
	return exitReasonStartFailed
}

// taskOOMKilled best-effort reports whether the container's cgroup recorded an
// OOM kill. Call it right after the task exits but before it is deleted (the
// cgroup still exists then). It returns false on any error so it can never
// misreport a plain crash as an OOM.
func taskOOMKilled(ctx context.Context, task containerd.Task) bool {
	if task == nil {
		return false
	}
	metric, err := task.Metrics(ctx)
	if err != nil || metric == nil {
		return false
	}
	if !typeurl.Is(metric.Data, (*cgroupv2.Metrics)(nil)) {
		return false // cgroup v1 has no equivalent counter we rely on here
	}
	m := &cgroupv2.Metrics{}
	if err := typeurl.UnmarshalTo(metric.Data, m); err != nil {
		return false
	}
	return m.GetMemoryEvents().GetOomKill() > 0
}

// recordContainerExit persists why containerID's last run ended, as container
// labels, so a stopped/crashed container can still explain itself after its
// task (and any live output stream) are gone. Best-effort: a missing container
// or a label-write failure is logged, never fatal — recording diagnostics must
// not perturb the lifecycle it observes.
func (c *Client) recordContainerExit(ctx context.Context, containerID string, code int32, reason, detail string, at time.Time) {
	ctx = c.withNamespace(ctx)
	ctr, err := c.client.LoadContainer(ctx, containerID)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			c.logger.Warn("Failed to load container to record exit",
				zap.String("container_id", containerID), zap.Error(err))
		}
		return
	}
	if len(detail) > exitDetailMaxLen {
		detail = detail[:exitDetailMaxLen]
	}
	updateErr := ctr.Update(ctx, func(ctx context.Context, client *containerd.Client, cc *containers.Container) error {
		if cc.Labels == nil {
			cc.Labels = map[string]string{}
		}
		cc.Labels[labelKeyExitCode] = strconv.Itoa(int(code))
		cc.Labels[labelKeyExitReason] = reason
		cc.Labels[labelKeyExitAt] = at.UTC().Format(time.RFC3339)
		if detail != "" {
			cc.Labels[labelKeyExitDetail] = detail
		} else {
			delete(cc.Labels, labelKeyExitDetail)
		}
		return nil
	})
	if updateErr != nil {
		c.logger.Warn("Failed to record container exit labels",
			zap.String("container_id", containerID), zap.Error(updateErr))
	}
}

// parseExitLabels reads the exit-diagnostics labels off a container's label
// map. ok is false when no exit was recorded (reason label absent), which the
// caller treats as "unknown" rather than "exited cleanly".
func parseExitLabels(labels map[string]string) (code int32, reason string, ok bool) {
	reason = labels[labelKeyExitReason]
	if reason == "" {
		return 0, "", false
	}
	if cs := labels[labelKeyExitCode]; cs != "" {
		if v, err := strconv.Atoi(cs); err == nil {
			code = int32(v)
		}
	}
	return code, reason, true
}

// recordStartFailure records that containerID's task never started, capturing
// the cause. Best-effort.
func (c *Client) recordStartFailure(ctx context.Context, containerID string, cause error) {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	c.recordContainerExit(ctx, containerID, exitCodeDidNotStart, classifyStartError(cause), detail, time.Now())
}
