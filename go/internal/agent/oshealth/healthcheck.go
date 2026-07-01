package oshealth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ServiceStatus is the verdict of a single service healthcheck.
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

const defaultPollInterval = 500 * time.Millisecond

// Checker polls systemd until critical services are active or their timeout
// expires.
type Checker struct {
	Logger       *zap.Logger
	PollInterval time.Duration
	// SystemctlShow returns the properties of a systemd unit as reported by
	// `systemctl show`; injectable for tests.
	SystemctlShow func(ctx context.Context, unit string) (map[string]string, error)
}

// NewChecker returns a Checker that queries systemd via systemctl.
func NewChecker(logger *zap.Logger) *Checker {
	return &Checker{
		Logger:        logger,
		PollInterval:  defaultPollInterval,
		SystemctlShow: systemctlShow,
	}
}

// CheckAll checks all services concurrently (total wall time is the slowest
// single check, not the sum) and returns one result per service, in input
// order.
func (c *Checker) CheckAll(ctx context.Context, services []CriticalService) []ServiceResult {
	results := make([]ServiceResult, len(services))
	var wg sync.WaitGroup
	for i, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = c.checkOne(ctx, svc)
		}()
	}
	wg.Wait()
	return results
}

func (c *Checker) checkOne(ctx context.Context, svc CriticalService) ServiceResult {
	interval := c.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	deadline := time.Now().Add(svc.Timeout)
	// Bound SystemctlShow by the service deadline: systemctl itself can hang
	// when systemd or D-Bus is unhealthy — exactly the boots this gate exists
	// for — and an unbounded call would block agent startup forever.
	checkCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	lastState := "no state observed"

	for {
		props, err := c.SystemctlShow(checkCtx, svc.Unit)
		switch {
		case err != nil:
			// systemctl itself may be briefly unavailable early in boot;
			// keep retrying until the timeout.
			lastState = fmt.Sprintf("systemctl error: %v", err)
		case props["LoadState"] == "not-found":
			return ServiceResult{Unit: svc.Unit, Status: StatusSkipped, Reason: "unit not present on this device"}
		case props["ActiveState"] == "active":
			return ServiceResult{Unit: svc.Unit, Status: StatusHealthy}
		case props["UnitFileState"] == "disabled" || props["UnitFileState"] == "masked" || props["LoadState"] == "masked":
			// A unit that ships on the image but is intentionally not enabled
			// must not block (and endlessly roll back) updates.
			return ServiceResult{
				Unit:   svc.Unit,
				Status: StatusSkipped,
				Reason: fmt.Sprintf("unit is %s on this device", props["UnitFileState"]),
			}
		default:
			lastState = fmt.Sprintf("ActiveState=%s SubState=%s", props["ActiveState"], props["SubState"])
			if result := props["Result"]; result != "" && result != "success" {
				lastState += fmt.Sprintf(" Result=%s", result)
			}
		}

		timedOut := ServiceResult{
			Unit:   svc.Unit,
			Status: StatusFailed,
			Reason: fmt.Sprintf("timed out after %s waiting for active; last state: %s", svc.Timeout, lastState),
		}
		if time.Now().After(deadline) {
			return timedOut
		}
		select {
		case <-checkCtx.Done():
			// checkCtx closes both when the caller cancels and when the
			// service deadline expires; only the former is an abort.
			if ctx.Err() != nil {
				return ServiceResult{
					Unit:   svc.Unit,
					Status: StatusFailed,
					Reason: fmt.Sprintf("check aborted: %v; last state: %s", ctx.Err(), lastState),
				}
			}
			return timedOut
		case <-time.After(interval):
		}
	}
}

// systemctlShow runs `systemctl show` for the unit and parses its KEY=VALUE
// output. The absolute path matters: the agent runs under systemd with a
// restricted PATH.
func systemctlShow(ctx context.Context, unit string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "show",
		"--property=LoadState,ActiveState,SubState,Result,UnitFileState", unit)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	// Don't wait forever for the killed process's pipes after cancellation.
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("systemctl show %s: %w", unit, err)
	}
	props := make(map[string]string)
	for line := range strings.Lines(string(out)) {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found {
			props[key] = value
		}
	}
	return props, nil
}

// envWithPath returns os.Environ() with the PATH entry replaced by the given
// value, mirroring the convention used for other system tools the agent execs.
func envWithPath(path string) []string {
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + path
			return env
		}
	}
	return append(env, "PATH="+path)
}
