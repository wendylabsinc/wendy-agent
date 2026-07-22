package commands

import (
	"context"
	"os/exec"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

// serviceHookRunner runs the per-service "wait for readiness → announce URL →
// fire postStart" sequence for multi-service runs (compose + services map).
// Semantics mirror the single-container paths: a readiness failure warns and
// still fires the hook; only context cancellation suppresses it.
//
// Zero-value-ready except conn: construct with &serviceHookRunner{conn: conn}.
type serviceHookRunner struct {
	conn *grpcclient.AgentConnection
	wg   sync.WaitGroup
	mu   sync.Mutex
	cmds []*exec.Cmd // cli-hook children to reap in attached mode
}

// runOne runs the readiness→announce→postStart sequence for a single service
// and blocks until it completes. ctx gates the readiness wait and the
// reachable-URL announcement; canceling it aborts both steps silently (no
// warning, no hook), matching Ctrl+C behavior on the single-container paths.
// hookCtx is the context the postStart CLI hook is spawned under: attached
// callers (via startAsync) pass the same runCtx as ctx, so canceling it after
// the run ends kills the hook too — mirroring run.go's
// `runCancel(); postStartCmd.Wait()`. Detached callers pass
// context.Background() as hookCtx so the hook outlives the CLI process, and
// never call reap since there is nothing left to wait on (openURL is
// synchronous, and cli hooks deliberately keep running).
//
// A nil cfg, or a cfg that declares neither Readiness nor Hooks, is a no-op:
// most services in a multi-service app don't opt into per-service lifecycle
// hooks, so runOne must not dial, warn, or print anything for them.
func (r *serviceHookRunner) runOne(ctx, hookCtx context.Context, cfg *appconfig.AppConfig) {
	if cfg == nil || (cfg.Readiness == nil && cfg.Hooks == nil) {
		return
	}

	if err := waitForReadiness(ctx, cfg.Readiness, r.conn.Host); err != nil {
		if ctx.Err() != nil {
			// Canceled (e.g. Ctrl+C, or the run ending) — stay silent and skip
			// the hook entirely; this is not a readiness failure to report.
			return
		}
		// containerExitDetail (invoked by warnReadiness) matches on the GROUP
		// appID: the agent's ListContainers groups per-service containers under
		// the group app-ID label, reports AppContainer.AppName as the bare
		// group appID, and aggregates exit code/reason onto that group entry —
		// so pass cfg.AppID, never cfg.ContainerName().
		warnReadiness(ctx, r.conn, cfg.AppID, err)
	}
	if ctx.Err() != nil {
		return
	}

	announceReachableURL(ctx, r.conn, cfg)

	cmd := startPostStartHook(hookCtx, cfg, r.conn.Host, cfg.ServiceName)
	if cmd != nil {
		r.mu.Lock()
		r.cmds = append(r.cmds, cmd)
		r.mu.Unlock()
	}
}

// startAsync runs runOne on a goroutine tracked by r.wg, using runCtx as both
// the readiness/announce context and the postStart hook's context. Attached
// multi-service paths use this so a slow or failing readiness probe on one
// service never delays starting the next; canceling runCtx once the overall
// run ends also terminates any still-running cli hook, so callers should
// follow up with reap() the same way run.go cancels runCtx before waiting on
// postStartCmd.
func (r *serviceHookRunner) startAsync(runCtx context.Context, cfg *appconfig.AppConfig) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.runOne(runCtx, runCtx, cfg)
	}()
}

// spawn runs fn on a goroutine tracked by r.wg, so a later reap() waits for it
// and any cli-hook children it registers via runOne. Unlike startAsync (which
// wraps a single runOne), spawn lets the caller gate runOne behind custom
// preconditions while keeping it part of the run's teardown — the compose
// app-level fallback uses it to wait for every service's Started ack, then
// re-check runCtx, before firing. Because reap() runs only after those Started
// acks have all been released and runCtx has been canceled, the spawned
// goroutine can never block reap indefinitely.
func (r *serviceHookRunner) spawn(fn func()) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		fn()
	}()
}

// reap waits for every startAsync'd runOne to finish, then waits on each
// tracked cli-hook child so its exit status is collected and no zombie is
// left behind. Callers invoke it after canceling runCtx, mirroring run.go's
// `runCancel(); postStartCmd.Wait()`. Wait errors are ignored — the caller
// only needs the process reaped, not its exit status.
func (r *serviceHookRunner) reap() {
	r.wg.Wait()
	r.mu.Lock()
	cmds := r.cmds
	r.mu.Unlock()
	for _, cmd := range cmds {
		_ = cmd.Wait()
	}
}

// appLevelLifecycleConfig returns a synthetic AppConfig carrying only the
// group identity and the top-level readiness/hooks, for the app-level
// fallback that fires once after ALL services have started. Returns nil when
// the config declares neither. Its hooks.postStart.agent is never sent to the
// agent (there is no app-level container); ValidateJSON already warns.
func appLevelLifecycleConfig(appID string, top *appconfig.AppConfig) *appconfig.AppConfig {
	if top == nil || (top.Readiness == nil && top.Hooks == nil) {
		return nil
	}
	return &appconfig.AppConfig{AppID: appID, Readiness: top.Readiness, Hooks: top.Hooks}
}
