# Incremental restart backoff for crash-looping containers

Date: 2026-08-09
Status: Approved, pending implementation
Area: `go/internal/agent/container/monitor.go`

## Problem

`ContainerMonitor` relaunches a stopped container on a flat cooldown. `planRestarts`
gates every restart on a single constant:

```go
if time.Since(state.LastRestart) < 10*time.Second {
    continue
}
```

An app that crashes immediately on start therefore restarts every ~10 seconds,
indefinitely. `FailureCount` climbs without bound and is never reset. Only
`RestartOnFailure` combined with an explicit `MaxRetries` ever stops; the default
policy (`RestartUnlessStopped`) and `RestartAlways` never do.

The consequences on a device are continuous churn from a permanently-broken app:
container create/start work, image and mount setup, log lines from both the agent
and the app, and whatever local endpoints the app touches during startup — roughly
360 restart cycles per hour, forever.

## Goals

- Slow repeated restarts of a container that keeps failing.
- Leave the timing of the first couple of attempts unchanged, so genuinely
  transient crashes still recover promptly.
- Let an app that recovers on its own return to normal restart behavior without
  operator action.
- Keep the change contained to the agent. No proto change, no CLI change.

## Non-goals

- Giving up on a crash-looping app. The monitor keeps retrying forever at the
  ceiling. Edge devices are unattended, and a dependency that comes back
  (network, camera, USB peripheral) must self-heal.
- Surfacing backoff state to the CLI. `wendy device apps` and the dashboard keep
  rendering exactly what they render today.
- Exit-code awareness. `RestartOnFailure` still behaves like
  `RestartUnlessStopped`, as documented in `shouldRestart`.

## Design

### State

Two fields are added to `containerState`:

```go
// BackoffLevel is the number of restarts performed since the last reset. It
// drives the delay before the next restart (see restartDelay).
BackoffLevel int

// RunningSince is the first tick at which the container was observed RUNNING
// since its last restart; zero while it is not running. Used to decide when a
// container has been healthy long enough to reset BackoffLevel.
RunningSince time.Time
```

`FailureCount` is deliberately left alone: cumulative and never reset. It is
user-visible through `RestartStatuses` → `container_service.go`, which feeds the
failure column in `wendy device apps` and the `crashLooping` flag. Keeping it
untouched means this change is invisible to every consumer outside the monitor.

### Delay curve

```go
const (
    restartBackoffBase = 10 * time.Second
    restartBackoffCap  = 5 * time.Minute
)

// restartDelay returns how long to wait before the next restart of a container
// that has already been restarted `level` times.
func restartDelay(level int) time.Duration
```

- `level <= 0` → `0` (first restart is immediate)
- otherwise `restartBackoffBase << (level-1)`, clamped to `restartBackoffCap`

| restart # | 1 | 2 | 3 | 4 | 5 | 6 | 7+ |
|-----------|---|---|---|---|---|---|-----|
| wait before it | 0 | 10s | 20s | 40s | 80s | 160s | 300s |

The first two attempts match today's timing exactly, so the common transient
crash is unaffected. Cumulative time to reach the ceiling is ~310s. Restarts in
the first hour of a permanent failure drop from ~360 to ~17.

`BackoffLevel` stops incrementing once `restartDelay(level) >= restartBackoffCap`,
so it cannot run away no matter how long a container loops.

`restartDelay` does not rely on that clamp for safety. It doubles by repeated
multiplication with an early exit at the cap rather than computing
`base << (level-1)`: a shift of 64 or more silently yields 0 on a 64-bit int,
which would hand back a zero delay and restore the exact unthrottled every-tick
restart this change exists to prevent. The loop exits within a handful of
iterations because it returns as soon as the cap is reached, so even
`math.MaxInt32` is cheap. This is covered by
`TestRestartDelay_LargeLevelStaysAtCap`.

### Gate and reset in `planRestarts`

The existing constant gate becomes level-aware:

```go
if time.Since(state.LastRestart) < restartDelay(state.BackoffLevel) {
    continue
}
```

When a restart is scheduled, alongside the existing `FailureCount++` and
`LastRestart = now`, `BackoffLevel` is incremented — but only while
`restartDelay(BackoffLevel) < restartBackoffCap`, so it stops growing once the
ceiling is reached rather than counting up forever. The existing "Restarting
container" log line gains the delay that was applied, so the growing backoff is
visible in agent logs.

The `running[appName]` branch, currently a bare `continue`, records stability:

- If `RunningSince` is zero, set it to now.
- Else if `now.Sub(RunningSince) >= restartStabilityWindow`, reset
  `BackoffLevel = 0`.

The not-running path clears `RunningSince` back to zero.

```go
const restartStabilityWindow = 60 * time.Second
```

The window is the crux of the design. The monitor ticks every 5s, so a container
that starts, is observed running once, and dies would otherwise earn a full reset
on every cycle and the backoff would stay pinned at 10s forever — precisely the
case this change exists to address. Requiring 60s of continuously-observed
RUNNING means only a container that is actually healthy gets its backoff cleared.

### Reset on redeploy

`Register` constructs a fresh `containerState`, so a redeploy resets the backoff
with no additional code.

`ClearExplicitStop` deliberately does **not** reset the backoff. A redeploy is
already covered by `Register`; a manual `start` of an app whose image has not
changed is still crash-looping and should keep its accumulated backoff. This also
avoids giving the failed-stop rollback path in `container_service.go` (which
calls `ClearExplicitStop` to revert marks) a side effect it does not want.

### Testability

`ContainerMonitor` calls `time.Now()` directly, and existing tests depend on a
zero `LastRestart` reading as "backoff already elapsed"
(`monitor_checkcontainers_test.go:306`). An unexported field is added:

```go
now func() time.Time // defaults to time.Now; overridden in tests
```

`NewContainerMonitor` defaults it. Backoff-growth and stability-window tests set
it to drive a fake clock instead of sleeping. Existing tests are unaffected.

## Testing

New tests in `go/internal/agent/container`:

1. **Backoff grows** — a container that stays down across successive ticks is
   restarted at 0, 10s, 20s, 40s, and not sooner.
2. **Cap holds** — after enough failures the delay stops at 5 min and stays
   there; `BackoffLevel` stops growing (guards against shift overflow).
3. **Stability resets** — a container observed RUNNING for ≥60s returns to a 0
   delay on its next failure.
4. **Brief liveness does not reset** — a container seen RUNNING for one tick and
   then down does not reset; the backoff continues to grow.
5. **`FailureCount` is unchanged** — it keeps incrementing per restart and is
   never reset, so `RestartStatuses` output is unaffected by a stability reset.
6. **Redeploy resets** — `Register` on an existing name clears the backoff.
7. **First restart is still immediate** — a freshly registered container that is
   down restarts on the first tick, matching today.

Existing `monitor_test.go` and `monitor_checkcontainers_test.go` must pass
unmodified. This was checked against the current tests: every case that expects a
restart uses a freshly-constructed monitor whose `LastRestart` is zero, and
`restartDelay(0)` is `0`, so none of them are gated by the new curve. The
suppression tests that call `planRestarts` twice (`monitor_test.go:550`, `:579`)
are also safe — a suppressed container returns before the `LastRestart` advance,
so the second call still sees a zero `LastRestart`.

## Known limitation (pre-existing, not addressed)

For shared-namespace app groups, `planRestartActions` collapses the down members
into one group restart that stops and recreates *every* member. A member that was
still running at plan time is therefore restarted without its own `LastRestart`
or `BackoffLevel` advancing. This is existing behavior; backoff neither improves
nor worsens it. Fixing it would mean advancing state for all group members at
plan time, which is out of scope here.

## Files touched

- `go/internal/agent/container/monitor.go` — state fields, `restartDelay`,
  constants, `planRestarts` gate and reset, clock field, log field.
- `go/internal/agent/container/monitor_backoff_test.go` — new tests.
