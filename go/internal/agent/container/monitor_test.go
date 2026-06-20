package container

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// fakeContainerdClient implements services.ContainerdClient by embedding it
// (so unimplemented methods panic if called) and also satisfies the
// groupRestarter capability the monitor type-asserts for shared-namespace
// group restarts. It records which appIDs were group-restarted.
type fakeContainerdClient struct {
	services.ContainerdClient
	groupOf       map[string]string // full container name -> bare appID for grouped members
	groupRestarts []string
}

func (f *fakeContainerdClient) GroupRestartAppID(_ context.Context, appName string) (string, bool) {
	id, ok := f.groupOf[appName]
	return id, ok
}

func (f *fakeContainerdClient) RestartGroup(_ context.Context, appID string) (map[string]<-chan services.ContainerOutput, error) {
	f.groupRestarts = append(f.groupRestarts, appID)
	return nil, nil
}

func TestRestartPolicy_String(t *testing.T) {
	tests := []struct {
		policy RestartPolicy
		want   string
	}{
		{RestartNo, "no"},
		{RestartUnlessStopped, "unless-stopped"},
		{RestartOnFailure, "on-failure"},
		{RestartAlways, "always"},
		{RestartPolicy(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.policy.String()
			if got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRestartPolicy(t *testing.T) {
	tests := []struct {
		input   string
		want    RestartPolicy
		wantErr bool
	}{
		{"no", RestartNo, false},
		{"", RestartNo, false},
		{"unless-stopped", RestartUnlessStopped, false},
		{"on-failure", RestartOnFailure, false},
		{"always", RestartAlways, false},
		{"invalid", RestartNo, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseRestartPolicy(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseRestartPolicy(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ParseRestartPolicy(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func newTestMonitor() *ContainerMonitor {
	logger := zap.NewNop()
	return NewContainerMonitor(logger, nil, nil, 1*time.Second)
}

func TestContainerMonitor_ShouldRestart_No(t *testing.T) {
	m := newTestMonitor()
	state := &containerState{
		RestartPolicy: RestartNo,
	}

	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true for RestartNo, want false")
	}
}

func TestContainerMonitor_ShouldRestart_UnlessStopped(t *testing.T) {
	m := newTestMonitor()

	// Should restart when not explicitly stopped.
	state := &containerState{
		RestartPolicy: RestartUnlessStopped,
		ExplicitStop:  false,
	}
	if !m.shouldRestart(state) {
		t.Error("shouldRestart() = false for UnlessStopped (not stopped), want true")
	}

	// Should not restart when explicitly stopped.
	state.ExplicitStop = true
	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true for UnlessStopped (explicitly stopped), want false")
	}
}

func TestContainerMonitor_ShouldRestart_OnFailure(t *testing.T) {
	m := newTestMonitor()

	// Should restart when under max retries.
	state := &containerState{
		RestartPolicy: RestartOnFailure,
		MaxRetries:    3,
		FailureCount:  1,
	}
	if !m.shouldRestart(state) {
		t.Error("shouldRestart() = false for OnFailure (under max retries), want true")
	}

	// Should not restart when at max retries.
	state.FailureCount = 3
	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true for OnFailure (at max retries), want false")
	}

	// Should not restart when explicitly stopped.
	state.FailureCount = 0
	state.ExplicitStop = true
	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true for OnFailure (explicitly stopped), want false")
	}

	// Zero max retries means unlimited retries.
	stateUnlimited := &containerState{
		RestartPolicy: RestartOnFailure,
		MaxRetries:    0,
		FailureCount:  100,
	}
	if !m.shouldRestart(stateUnlimited) {
		t.Error("shouldRestart() = false for OnFailure (unlimited retries), want true")
	}
}

func TestContainerMonitor_ExplicitStop(t *testing.T) {
	m := newTestMonitor()

	m.Register("test-app", RestartUnlessStopped, 0)

	// Mark as explicitly stopped.
	m.MarkExplicitStop("test-app")

	m.mu.Lock()
	state, ok := m.states["test-app"]
	m.mu.Unlock()

	if !ok {
		t.Fatal("test-app not found in states")
	}
	if !state.ExplicitStop {
		t.Error("ExplicitStop = false after MarkExplicitStop, want true")
	}

	// Should not restart.
	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true after explicit stop, want false")
	}
}

// TestPlanRestarts_DoesNotRestartRunningMultiServiceMembers guards against the
// monitor force-restarting healthy multi-service containers. Each service is
// monitored under its full container name ("{appID}_{serviceName}"), but
// ListContainers reports the app under its bare appID with an aggregate running
// state. The monitor must reconcile per-service state, not the bare appID, or it
// treats every running service as stopped and restarts the whole group on every
// tick (killing healthy containers).
func TestPlanRestarts_DoesNotRestartRunningMultiServiceMembers(t *testing.T) {
	m := newTestMonitor()
	m.Register("sh.wendy.examples.ros2_talker", RestartUnlessStopped, 0)
	m.Register("sh.wendy.examples.ros2_listener", RestartUnlessStopped, 0)

	containers := []*agentpb.AppContainer{
		{
			AppName:      "sh.wendy.examples.ros2",
			RunningState: agentpb.AppRunningState_RUNNING,
			Services: []*agentpb.ServiceEntry{
				{Name: "talker", RunningState: agentpb.AppRunningState_RUNNING},
				{Name: "listener", RunningState: agentpb.AppRunningState_RUNNING},
			},
		},
	}

	got := m.planRestarts(containers)
	if len(got) != 0 {
		t.Errorf("planRestarts restarted running multi-service members %v; want none", got)
	}
}

// TestPlanRestarts_RestartsStoppedMultiServiceMember verifies the per-service
// reconciliation still restarts a genuinely stopped service even when a sibling
// in the same app is running (so the aggregate AppContainer state is RUNNING).
func TestPlanRestarts_RestartsStoppedMultiServiceMember(t *testing.T) {
	m := newTestMonitor()
	m.Register("app_talker", RestartUnlessStopped, 0)
	m.Register("app_listener", RestartUnlessStopped, 0)

	containers := []*agentpb.AppContainer{
		{
			AppName:      "app",
			RunningState: agentpb.AppRunningState_RUNNING, // aggregate: talker is up
			Services: []*agentpb.ServiceEntry{
				{Name: "talker", RunningState: agentpb.AppRunningState_RUNNING},
				{Name: "listener", RunningState: agentpb.AppRunningState_STOPPED},
			},
		},
	}

	got := m.planRestarts(containers)
	if len(got) != 1 || got[0] != "app_listener" {
		t.Errorf("planRestarts = %v; want [app_listener]", got)
	}
}

// TestPlanRestartActions_CoalescesGroupMembers verifies that when several
// members of the same shared-namespace group are due for restart, they collapse
// to a single group restart (not one independent restart per member, which would
// strand secondaries in a dead namespace).
func TestPlanRestartActions_CoalescesGroupMembers(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{
		"app_talker":   "app",
		"app_listener": "app",
	}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	actions := m.planRestartActions(context.Background(), []string{"app_talker", "app_listener"})

	var groups, singles []string
	for _, a := range actions {
		if a.groupAppID != "" {
			groups = append(groups, a.groupAppID)
		} else {
			singles = append(singles, a.single)
		}
	}
	if len(groups) != 1 || groups[0] != "app" {
		t.Errorf("group actions = %v; want [app]", groups)
	}
	if len(singles) != 0 {
		t.Errorf("single actions = %v; want none", singles)
	}
}

// TestPlanRestartActions_SingleForNonGroupedContainer verifies a container that
// is not part of a shared-namespace group is restarted on its own.
func TestPlanRestartActions_SingleForNonGroupedContainer(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	actions := m.planRestartActions(context.Background(), []string{"solo-app"})

	if len(actions) != 1 || actions[0].single != "solo-app" || actions[0].groupAppID != "" {
		t.Errorf("actions = %+v; want one single restart of solo-app", actions)
	}
}

// TestRestartGroup_SkipsWhenAlreadyInProgress verifies the monitor will not
// launch a second restart of a group while one is already running (which would
// race two stop/start cycles on the same primary).
func TestRestartGroup_SkipsWhenAlreadyInProgress(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	m.mu.Lock()
	m.groupRestarting["app"] = true // simulate an in-flight restart
	m.mu.Unlock()

	m.restartGroup(context.Background(), "app")

	if len(fake.groupRestarts) != 0 {
		t.Errorf("restartGroup ran while one was already in progress: %v", fake.groupRestarts)
	}
}

// TestRestartGroup_RunsAndClearsFlag verifies a group restart calls through to
// the client exactly once and clears the in-progress flag when it returns.
func TestRestartGroup_RunsAndClearsFlag(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	m.restartGroup(context.Background(), "app")

	if len(fake.groupRestarts) != 1 || fake.groupRestarts[0] != "app" {
		t.Errorf("RestartGroup calls = %v; want [app]", fake.groupRestarts)
	}
	m.mu.Lock()
	inProgress := m.groupRestarting["app"]
	m.mu.Unlock()
	if inProgress {
		t.Error("groupRestarting flag not cleared after restartGroup returned")
	}
}

func TestContainerMonitor_Register_And_Unregister(t *testing.T) {
	m := newTestMonitor()

	m.Register("app-1", RestartAlways, 0)
	m.Register("app-2", RestartOnFailure, 5)

	m.mu.Lock()
	if len(m.states) != 2 {
		t.Errorf("states count = %d, want 2", len(m.states))
	}
	m.mu.Unlock()

	m.Unregister("app-1")

	m.mu.Lock()
	if len(m.states) != 1 {
		t.Errorf("states count after unregister = %d, want 1", len(m.states))
	}
	if _, ok := m.states["app-1"]; ok {
		t.Error("app-1 still in states after Unregister")
	}
	m.mu.Unlock()
}
