package services

import (
	"testing"

	"go.uber.org/zap"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeRestartStatusProvider is a ContainerMonitorRegistrar that also exposes
// restart bookkeeping, so applyRestartStatus can be exercised without a real
// monitor. The embedded no-op registrar keeps it satisfying the interface the
// service stores.
type fakeRestartStatusProvider struct {
	noopRegistrar
	statuses map[string]RestartStatus
}

func (f fakeRestartStatusProvider) RestartStatuses() map[string]RestartStatus {
	return f.statuses
}

// noopRegistrar satisfies ContainerMonitorRegistrar with no behaviour.
type noopRegistrar struct{}

func (noopRegistrar) Register(string, int, int) {}
func (noopRegistrar) Unregister(string)         {}
func (noopRegistrar) MarkExplicitStop(string)   {}
func (noopRegistrar) ClearExplicitStop(string)  {}

func newRestartStatusService(t *testing.T, statuses map[string]RestartStatus) *ContainerService {
	t.Helper()
	return &ContainerService{
		logger:  zap.NewNop(),
		monitor: fakeRestartStatusProvider{statuses: statuses},
	}
}

func TestApplyRestartStatus_UpgradesStoppedSingleContainerToCrashLooping(t *testing.T) {
	s := newRestartStatusService(t, map[string]RestartStatus{
		"myapp": {FailureCount: 40, WillRestart: true},
	})
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_STOPPED},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_CRASH_LOOPING {
		t.Fatalf("running state = %v, want CRASH_LOOPING", got)
	}
	if got := containers[0].GetFailureCount(); got != 40 {
		t.Fatalf("failure count = %d, want 40", got)
	}
}

func TestApplyRestartStatus_RunningStaysRunning(t *testing.T) {
	// A running task with a non-zero historical failure count must not be
	// downgraded to CRASH_LOOPING: it is up right now. Failure count is still
	// surfaced for context.
	s := newRestartStatusService(t, map[string]RestartStatus{
		"myapp": {FailureCount: 3, WillRestart: true},
	})
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_RUNNING},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_RUNNING {
		t.Fatalf("running state = %v, want RUNNING", got)
	}
	if got := containers[0].GetFailureCount(); got != 3 {
		t.Fatalf("failure count = %d, want 3", got)
	}
}

func TestApplyRestartStatus_StoppedByUserStaysStopped(t *testing.T) {
	// Explicitly stopped app: monitor reports WillRestart=false. It must read
	// STOPPED, not CRASH_LOOPING, even with a historical failure count.
	s := newRestartStatusService(t, map[string]RestartStatus{
		"myapp": {FailureCount: 5, WillRestart: false},
	})
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_STOPPED},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_STOPPED {
		t.Fatalf("running state = %v, want STOPPED", got)
	}
}

func TestApplyRestartStatus_FirstExitBeforeAnyRestartStaysStopped(t *testing.T) {
	// FailureCount 0 means the monitor hasn't restarted it yet — a plain stop,
	// not a crash loop.
	s := newRestartStatusService(t, map[string]RestartStatus{
		"myapp": {FailureCount: 0, WillRestart: true},
	})
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_STOPPED},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_STOPPED {
		t.Fatalf("running state = %v, want STOPPED", got)
	}
}

func TestApplyRestartStatus_ServicesMapAggregatesAndMarksMember(t *testing.T) {
	// Group with one healthy service and one crash-looping service: the crashing
	// member's entry flips to CRASH_LOOPING, failure_count aggregates, and the
	// top-level app stays RUNNING because a member is still up.
	s := newRestartStatusService(t, map[string]RestartStatus{
		"app_web": {FailureCount: 0, WillRestart: true},
		"app_llm": {FailureCount: 12, WillRestart: true},
	})
	containers := []*agentpb.AppContainer{
		{
			AppName:      "app",
			RunningState: agentpb.AppRunningState_RUNNING,
			Services: []*agentpb.ServiceEntry{
				{Name: "web", RunningState: agentpb.AppRunningState_RUNNING},
				{Name: "llm", RunningState: agentpb.AppRunningState_STOPPED},
			},
		},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_RUNNING {
		t.Fatalf("app running state = %v, want RUNNING (one member up)", got)
	}
	if got := containers[0].GetFailureCount(); got != 12 {
		t.Fatalf("aggregate failure count = %d, want 12", got)
	}
	svcs := containers[0].GetServices()
	if got := svcs[0].GetRunningState(); got != agentpb.AppRunningState_RUNNING {
		t.Fatalf("web service state = %v, want RUNNING", got)
	}
	if got := svcs[1].GetRunningState(); got != agentpb.AppRunningState_CRASH_LOOPING {
		t.Fatalf("llm service state = %v, want CRASH_LOOPING", got)
	}
}

func TestApplyRestartStatus_ServicesMapAllStoppedUpgradesTopLevel(t *testing.T) {
	s := newRestartStatusService(t, map[string]RestartStatus{
		"app_llm": {FailureCount: 8, WillRestart: true},
	})
	containers := []*agentpb.AppContainer{
		{
			AppName:      "app",
			RunningState: agentpb.AppRunningState_STOPPED,
			Services: []*agentpb.ServiceEntry{
				{Name: "llm", RunningState: agentpb.AppRunningState_STOPPED},
			},
		},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_CRASH_LOOPING {
		t.Fatalf("app running state = %v, want CRASH_LOOPING", got)
	}
}

func TestApplyRestartStatus_NoProviderIsNoop(t *testing.T) {
	// Monitor that doesn't implement RestartStatusProvider: listing is untouched.
	s := &ContainerService{logger: zap.NewNop(), monitor: noopRegistrar{}}
	containers := []*agentpb.AppContainer{
		{AppName: "myapp", RunningState: agentpb.AppRunningState_STOPPED},
	}

	s.applyRestartStatus(containers)

	if got := containers[0].GetRunningState(); got != agentpb.AppRunningState_STOPPED {
		t.Fatalf("running state = %v, want STOPPED unchanged", got)
	}
}
