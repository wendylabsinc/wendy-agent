package container

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

const backoffTestApp = "com.example.app"

// newBackoffMonitor returns a monitor whose clock is driven by the returned
// pointer, plus the stopped/running container snapshots for backoffTestApp.
// Tests advance time by writing through the pointer instead of sleeping.
func newBackoffMonitor(t *testing.T) (m *ContainerMonitor, clock *time.Time, stopped, running []*agentpb.AppContainer) {
	t.Helper()
	m = newMonitorWithClient(&fakeContainerd{})
	// A fixed, arbitrary wall-clock origin; only deltas matter.
	now := time.Unix(1700000000, 0)
	clock = &now
	m.now = func() time.Time { return *clock }
	m.Register(backoffTestApp, RestartUnlessStopped, 0)

	stopped = []*agentpb.AppContainer{{
		AppName:      backoffTestApp,
		RunningState: agentpb.AppRunningState_STOPPED,
	}}
	running = []*agentpb.AppContainer{{
		AppName:      backoffTestApp,
		RunningState: agentpb.AppRunningState_RUNNING,
	}}
	return m, clock, stopped, running
}

// backoffLevel reads the monitor's private backoff bookkeeping for the test app.
func backoffLevel(t *testing.T, m *ContainerMonitor) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[backoffTestApp]
	if !ok {
		t.Fatalf("%s is not registered", backoffTestApp)
	}
	return st.BackoffLevel
}

func failureCount(t *testing.T, m *ContainerMonitor) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[backoffTestApp]
	if !ok {
		t.Fatalf("%s is not registered", backoffTestApp)
	}
	return st.FailureCount
}

// mustRestart asserts planRestarts schedules exactly the test app.
func mustRestart(t *testing.T, m *ContainerMonitor, containers []*agentpb.AppContainer, msg string) {
	t.Helper()
	got := m.planRestarts(containers)
	if len(got) != 1 || got[0] != backoffTestApp {
		t.Fatalf("%s: planRestarts = %v; want [%s]", msg, got, backoffTestApp)
	}
}

// mustNotRestart asserts planRestarts schedules nothing.
func mustNotRestart(t *testing.T, m *ContainerMonitor, containers []*agentpb.AppContainer, msg string) {
	t.Helper()
	if got := m.planRestarts(containers); len(got) != 0 {
		t.Fatalf("%s: planRestarts = %v; want no restart", msg, got)
	}
}

// The backoff curve doubles from a 10s base and clamps at the 5m ceiling. The
// first two restarts (levels 0 and 1) must keep the pre-backoff timing so a
// transient crash still recovers promptly.
func TestRestartDelay_Curve(t *testing.T) {
	tests := []struct {
		name  string
		level int
		want  time.Duration
	}{
		{name: "first restart is immediate", level: 0, want: 0},
		{name: "second restart keeps the legacy 10s cooldown", level: 1, want: 10 * time.Second},
		{name: "third doubles", level: 2, want: 20 * time.Second},
		{name: "fourth doubles", level: 3, want: 40 * time.Second},
		{name: "fifth doubles", level: 4, want: 80 * time.Second},
		{name: "sixth doubles", level: 5, want: 160 * time.Second},
		{name: "seventh would be 320s and clamps to the cap", level: 6, want: restartBackoffCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restartDelay(tt.level); got != tt.want {
				t.Errorf("restartDelay(%d) = %v; want %v", tt.level, got, tt.want)
			}
		})
	}
}

// A container can loop for days. restartDelay must stay pinned at the cap for
// any level, including ones that would overflow a naive `base << (level-1)`
// shift (a shift of 64+ wraps to 0 on amd64/arm64, which would restore the
// unthrottled every-tick restart this whole change exists to prevent).
func TestRestartDelay_LargeLevelStaysAtCap(t *testing.T) {
	for _, level := range []int{7, 20, 62, 63, 64, 65, 128, 1000, math.MaxInt32} {
		if got := restartDelay(level); got != restartBackoffCap {
			t.Errorf("restartDelay(%d) = %v; want %v (cap)", level, got, restartBackoffCap)
		}
	}
}

// Defensive: a negative level must not panic or produce a negative delay, which
// would make the gate in planRestarts always pass.
func TestRestartDelay_NegativeLevelIsImmediate(t *testing.T) {
	for _, level := range []int{-1, -100, math.MinInt32} {
		if got := restartDelay(level); got != 0 {
			t.Errorf("restartDelay(%d) = %v; want 0", level, got)
		}
	}
}

// A container that never comes up must be restarted on a widening interval, and
// must not be restarted early. Before this change the gate was a flat 10s, so
// the 20s/40s/80s steps below all restarted a tick too soon.
func TestPlanRestarts_BackoffWidensWhileDown(t *testing.T) {
	m, clock, stopped, _ := newBackoffMonitor(t)

	// Wait required before each successive restart.
	for i, want := range []time.Duration{0, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second} {
		if want > 0 {
			*clock = clock.Add(want - time.Second)
			mustNotRestart(t, m, stopped, "one second short of the backoff")
			*clock = clock.Add(time.Second)
		}
		mustRestart(t, m, stopped, fmt.Sprintf("restart %d once the backoff elapsed", i+1))
	}
}

// The delay stops doubling at the ceiling and the monitor keeps retrying there
// forever. BackoffLevel must stop growing too, so it cannot run away.
func TestPlanRestarts_BackoffStopsAtCap(t *testing.T) {
	m, clock, stopped, _ := newBackoffMonitor(t)

	// Drive it well past the ceiling.
	for i := 0; i < 20; i++ {
		*clock = clock.Add(restartBackoffCap)
		mustRestart(t, m, stopped, fmt.Sprintf("restart %d at the ceiling", i+1))
	}

	// Still exactly the cap — no more, and no less.
	*clock = clock.Add(restartBackoffCap - time.Second)
	mustNotRestart(t, m, stopped, "one second short of the ceiling")
	*clock = clock.Add(time.Second)
	mustRestart(t, m, stopped, "once the ceiling elapsed")

	// Level 6 is the first whose delay is already the cap, so incrementing
	// stops there.
	if got, want := backoffLevel(t, m), 6; got != want {
		t.Errorf("BackoffLevel = %d after 20+ restarts; want it clamped at %d", got, want)
	}
}

// A container that comes up and stays up long enough is healthy: its next
// failure should restart promptly rather than inheriting the old long delay.
func TestPlanRestarts_StabilityWindowResetsBackoff(t *testing.T) {
	m, clock, stopped, running := newBackoffMonitor(t)

	// Build up some backoff.
	for _, wait := range []time.Duration{0, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		*clock = clock.Add(wait)
		mustRestart(t, m, stopped, "building up backoff")
	}
	if got := backoffLevel(t, m); got == 0 {
		t.Fatalf("BackoffLevel = 0 after four restarts; test cannot detect a reset")
	}

	// Healthy for the full stability window, observed across several ticks.
	for elapsed := time.Duration(0); elapsed <= restartStabilityWindow; elapsed += 5 * time.Second {
		mustNotRestart(t, m, running, "running container must not restart")
		*clock = clock.Add(5 * time.Second)
	}
	if got := backoffLevel(t, m); got != 0 {
		t.Fatalf("BackoffLevel = %d after %v of continuous health; want 0", got, restartStabilityWindow)
	}

	// It dies again: back to the base curve, not the accumulated one.
	mustRestart(t, m, stopped, "first restart after recovery is immediate")
	*clock = clock.Add(9 * time.Second)
	mustNotRestart(t, m, stopped, "9s after recovery restart")
	*clock = clock.Add(time.Second)
	mustRestart(t, m, stopped, "10s after recovery restart (base delay, not the pre-reset one)")
}

// The crux: a container that survives a single tick and dies is still
// crash-looping. If a momentary RUNNING sighting reset the backoff, the delay
// would stay pinned at the base forever and the feature would do nothing.
func TestPlanRestarts_BriefLivenessDoesNotResetBackoff(t *testing.T) {
	m, clock, stopped, running := newBackoffMonitor(t)

	for _, wait := range []time.Duration{0, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		*clock = clock.Add(wait)
		mustRestart(t, m, stopped, "building up backoff")
	}
	levelBefore := backoffLevel(t, m)

	// Seen running for a single 5s tick, then gone again.
	mustNotRestart(t, m, running, "running for one tick")
	*clock = clock.Add(5 * time.Second)

	if got := backoffLevel(t, m); got != levelBefore {
		t.Errorf("BackoffLevel = %d after one running tick; want it unchanged at %d", got, levelBefore)
	}
	// And the long delay still applies: 80s was next, so 79s must be too soon.
	*clock = clock.Add(74 * time.Second)
	mustNotRestart(t, m, stopped, "79s after the last restart, with an 80s backoff")
}

// FailureCount is user-visible through RestartStatuses (the failure column in
// `wendy device apps` and the crash-looping flag). A stability reset clears the
// backoff only — the reported failure tally must be untouched.
func TestPlanRestarts_StabilityResetKeepsFailureCount(t *testing.T) {
	m, clock, stopped, running := newBackoffMonitor(t)

	for _, wait := range []time.Duration{0, 10 * time.Second, 20 * time.Second} {
		*clock = clock.Add(wait)
		mustRestart(t, m, stopped, "building up backoff")
	}
	if got, want := failureCount(t, m), 3; got != want {
		t.Fatalf("FailureCount = %d after three restarts; want %d", got, want)
	}
	// Precondition: there must be backoff to reset, otherwise the assertion
	// below that FailureCount survived a reset would hold vacuously.
	if got := backoffLevel(t, m); got == 0 {
		t.Fatalf("BackoffLevel = 0 after three restarts; nothing for the window to reset")
	}

	for elapsed := time.Duration(0); elapsed <= restartStabilityWindow; elapsed += 5 * time.Second {
		mustNotRestart(t, m, running, "running container must not restart")
		*clock = clock.Add(5 * time.Second)
	}

	if got := backoffLevel(t, m); got != 0 {
		t.Fatalf("BackoffLevel = %d; want 0 (precondition: the reset happened)", got)
	}
	if got, want := failureCount(t, m), 3; got != want {
		t.Errorf("FailureCount = %d after a stability reset; want it preserved at %d", got, want)
	}
}

// A redeploy goes through Register, which must hand the app a clean slate
// rather than making a freshly-deployed fix wait out the old backoff.
func TestRegister_ResetsBackoff(t *testing.T) {
	m, clock, stopped, _ := newBackoffMonitor(t)

	for _, wait := range []time.Duration{0, 10 * time.Second, 20 * time.Second, 40 * time.Second} {
		*clock = clock.Add(wait)
		mustRestart(t, m, stopped, "building up backoff")
	}
	if got := backoffLevel(t, m); got == 0 {
		t.Fatalf("BackoffLevel = 0 after four restarts; test cannot detect a reset")
	}

	m.Register(backoffTestApp, RestartUnlessStopped, 0)

	if got := backoffLevel(t, m); got != 0 {
		t.Errorf("BackoffLevel = %d after Register; want 0", got)
	}
	mustRestart(t, m, stopped, "restart immediately after redeploy")
}
