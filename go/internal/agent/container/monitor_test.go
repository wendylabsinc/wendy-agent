package container

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// errRestartGroup is a stand-in for a partial group-restart failure.
var errRestartGroup = errors.New("secondary failed to start")

// fakeContainerdClient implements services.ContainerdClient by embedding it
// (so unimplemented methods panic if called) and also satisfies the
// groupRestarter capability the monitor type-asserts for shared-namespace
// group restarts. It records which appIDs were group-restarted.
type fakeContainerdClient struct {
	services.ContainerdClient
	groupOf           map[string]string // full container name -> bare appID for grouped members
	groupRestarts     []string
	restartGroupChans map[string]<-chan services.ContainerOutput
	restartGroupErr   error
}

func (f *fakeContainerdClient) GroupRestartAppID(_ context.Context, appName string) (string, bool) {
	id, ok := f.groupOf[appName]
	return id, ok
}

func (f *fakeContainerdClient) RestartGroup(_ context.Context, appID string) (map[string]<-chan services.ContainerOutput, error) {
	f.groupRestarts = append(f.groupRestarts, appID)
	return f.restartGroupChans, f.restartGroupErr
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

// TestContainerMonitor_ShouldRestart_Always verifies RestartAlways handles
// spontaneous exits but still yields to an explicit user stop.
func TestContainerMonitor_ShouldRestart_Always(t *testing.T) {
	m := newTestMonitor()

	state := &containerState{RestartPolicy: RestartAlways}
	if !m.shouldRestart(state) {
		t.Error("shouldRestart() = false for RestartAlways (not stopped), want true")
	}

	state.ExplicitStop = true
	if m.shouldRestart(state) {
		t.Error("shouldRestart() = true for explicitly-stopped RestartAlways, want false")
	}
}

// TestRestartBlocked_AlwaysPolicyHonorsExplicitStop guards the direct-start
// race between a monitor tick and a user stop.
func TestRestartBlocked_AlwaysPolicyHonorsExplicitStop(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartAlways, 0)
	m.MarkExplicitStop("com.example.app")

	if !m.restartBlocked("com.example.app") {
		t.Error("restartBlocked() = false for RestartAlways + ExplicitStop; want true")
	}
}

// TestRestartBlocked_OnFailurePolicyHonorsExplicitStop is the paired case: a
// policy shouldRestart does honor ExplicitStop for must still be blocked.
func TestRestartBlocked_OnFailurePolicyHonorsExplicitStop(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartOnFailure, 0)
	m.MarkExplicitStop("com.example.app")

	if !m.restartBlocked("com.example.app") {
		t.Error("restartBlocked() = false for RestartOnFailure + ExplicitStop; want true")
	}
}

// TestRestartBlocked_SuppressedAlwaysStillBlocked verifies the suppression
// arm stays unconditional: suppression means a replace/stop operation is
// actively tearing the task down right now, which blocks every policy
// including RestartAlways — it is orthogonal to whether the policy honors
// ExplicitStop.
func TestRestartBlocked_SuppressedAlwaysStillBlocked(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartAlways, 0)
	resume := m.Suppress("com.example.app")
	defer resume()

	if !m.restartBlocked("com.example.app") {
		t.Error("restartBlocked() = false for suppressed RestartAlways container; want true (suppression is unconditional)")
	}
}

// TestPlanRestarts_ExplicitlyStoppedAlwaysPolicyStaysStopped is the group-stop
// regression: an `always` member must not schedule a group restart after a
// direct user stop.
func TestPlanRestarts_ExplicitlyStoppedAlwaysPolicyStaysStopped(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartAlways, 0)
	m.MarkExplicitStop("com.example.app")

	stopped := []*agentpb.AppContainer{
		{AppName: "com.example.app", RunningState: agentpb.AppRunningState_STOPPED},
	}

	got := m.planRestarts(stopped)
	if len(got) != 0 {
		t.Errorf("planRestarts = %v; want none for explicitly-stopped RestartAlways", got)
	}

	m.mu.Lock()
	fc := m.states["com.example.app"].FailureCount
	m.mu.Unlock()
	if fc != 0 {
		t.Errorf("FailureCount = %d after explicit stop; want 0", fc)
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

// TestPlanRestartActions_DefersGroupWhenMemberSuppressed is the regression
// guard for the group-restart-bypasses-suppression gap (F2 round 1 follow-up,
// finding 1): planRestarts/Suppress only ever gate individual member names,
// but planRestartActions escalates any one unsuppressed, down member into a
// whole-group restartGroup(appID) call — whose stopOne/refreshSecondaryNamespaces
// would stop/recreate every member, including a sibling a replace/stop is
// currently holding suppressed. A suppressed member must defer the entire
// group action until it is resumed, even when that member itself isn't in
// toRestart (e.g. it's still reporting RUNNING because the caller hasn't
// killed its task yet).
func TestPlanRestartActions_DefersGroupWhenMemberSuppressed(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{
		"app_talker":   "app",
		"app_listener": "app",
	}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	resume := m.Suppress("app_talker") // e.g. a replace mid-teardown of talker

	actions := m.planRestartActions(context.Background(), []string{"app_listener"})
	if len(actions) != 0 {
		t.Errorf("planRestartActions = %+v while a group member is suppressed; want no action", actions)
	}

	resume()

	actions = m.planRestartActions(context.Background(), []string{"app_listener"})
	if len(actions) != 1 || actions[0].groupAppID != "app" {
		t.Errorf("planRestartActions after resume = %+v; want one group action for app", actions)
	}
}

func TestPlanRestartActions_DefersGroupWhenMemberExplicitlyStopped(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{
		"app_talker":   "app",
		"app_listener": "app",
	}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)
	m.Register("app_talker", RestartAlways, 0)
	m.MarkExplicitStop("app_talker")

	actions := m.planRestartActions(context.Background(), []string{"app_listener"})
	if len(actions) != 0 {
		t.Errorf("planRestartActions = %+v with explicitly-stopped sibling; want no action", actions)
	}
}

func TestRestartGroup_SkipsWhenMemberStoppedAfterScheduling(t *testing.T) {
	fake := &fakeContainerdClient{groupOf: map[string]string{
		"app_talker":   "app",
		"app_listener": "app",
	}}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)
	m.Register("app_talker", RestartAlways, 0)
	m.MarkExplicitStop("app_talker")

	m.restartGroup(context.Background(), "app")
	if len(fake.groupRestarts) != 0 {
		t.Errorf("RestartGroup calls = %v; want none", fake.groupRestarts)
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

// TestRestartGroup_DrainsChannelsOnError is the WDY-1822 regression guard.
// RestartGroup can return partially-started services together with an error
// (e.g. the primary started but a secondary failed). If the monitor returns
// early on error instead of draining the returned channels, the abandoned
// channel back-pressures through the agent's pipes into the service's stdout
// FIFO and freezes the process in pipe_write once the buffers fill. This test
// hands restartGroup a live channel alongside an error and asserts the channel
// is fully consumed; it fails (times out) if the drain-on-error fix is reverted.
func TestRestartGroup_DrainsChannelsOnError(t *testing.T) {
	// Unbuffered: the sender only completes if a drain consumes every item, so
	// an undrained channel leaves the sender blocked and the test detects it.
	ch := make(chan services.ContainerOutput)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for i := 0; i < 3; i++ {
			ch <- services.ContainerOutput{Stdout: []byte("line")}
		}
		close(ch)
	}()

	fake := &fakeContainerdClient{
		groupOf: map[string]string{},
		restartGroupChans: map[string]<-chan services.ContainerOutput{
			"app_talker": ch,
		},
		restartGroupErr: errRestartGroup,
	}
	m := NewContainerMonitor(zap.NewNop(), fake, nil, time.Second)

	m.restartGroup(context.Background(), "app")

	select {
	case <-drained:
		// Channel fully consumed: the monitor drained output despite the error.
	case <-time.After(2 * time.Second):
		t.Fatal("restartGroup did not drain the returned channel on error (WDY-1822 back-pressure regression)")
	}

	if len(fake.groupRestarts) != 1 || fake.groupRestarts[0] != "app" {
		t.Errorf("RestartGroup calls = %v; want [app]", fake.groupRestarts)
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

// TestMonitorSuppressSkipsRestart is the regression guard for F2: while a
// Suppress handle is held for a container name, planRestarts must not
// schedule a restart for it even though the container is stopped and its
// policy would otherwise restart it. This is what lets a replace/stop
// operation kill and delete the task without the monitor's next tick
// resurrecting it mid-teardown (observed live: "cannot delete running task:
// failed precondition"). Once the handle is resumed, the same input restarts
// normally again.
func TestMonitorSuppressSkipsRestart(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartUnlessStopped, 0)

	stopped := []*agentpb.AppContainer{
		{AppName: "com.example.app", RunningState: agentpb.AppRunningState_STOPPED},
	}

	resume := m.Suppress("com.example.app")

	if got := m.planRestarts(stopped); len(got) != 0 {
		t.Errorf("planRestarts scheduled a restart while suppressed: %v", got)
	}

	resume()

	got := m.planRestarts(stopped)
	if len(got) != 1 || got[0] != "com.example.app" {
		t.Errorf("planRestarts after resume = %v; want [com.example.app]", got)
	}
}

// TestMonitorSuppressIsReferenceCounted verifies two independent Suppress
// handles on the same container name (e.g. a stop racing a replace of the
// same service) both have to resume before restarts are allowed again —
// otherwise the first operation to finish would silently re-enable restarts
// while the second is still mid-teardown.
func TestMonitorSuppressIsReferenceCounted(t *testing.T) {
	m := newTestMonitor()
	m.Register("com.example.app", RestartUnlessStopped, 0)

	stopped := []*agentpb.AppContainer{
		{AppName: "com.example.app", RunningState: agentpb.AppRunningState_STOPPED},
	}

	resumeA := m.Suppress("com.example.app")
	resumeB := m.Suppress("com.example.app")

	resumeA()
	if got := m.planRestarts(stopped); len(got) != 0 {
		t.Errorf("planRestarts scheduled a restart with one of two suppressions still held: %v", got)
	}

	resumeB()
	if got := m.planRestarts(stopped); len(got) != 1 || got[0] != "com.example.app" {
		t.Errorf("planRestarts after both resumed = %v; want [com.example.app]", got)
	}
}

// TestMonitorSuppressResumeIsIdempotent verifies calling resume more than
// once does not under-flow the suppression counter, which would otherwise let
// a spurious extra resume cancel out a still-active, unrelated Suppress call
// on the same name.
func TestMonitorSuppressResumeIsIdempotent(t *testing.T) {
	m := newTestMonitor()

	resume := m.Suppress("com.example.app")
	resume()
	resume() // must be a no-op, not decrement past zero

	m.mu.Lock()
	count := m.suppressed["com.example.app"]
	m.mu.Unlock()
	if count != 0 {
		t.Errorf("suppressed count = %d after idempotent double-resume; want 0", count)
	}
}

func TestContainerMonitor_RestartStatuses(t *testing.T) {
	m := newTestMonitor()

	// Actively restart-looping app: unless-stopped policy, several failures.
	m.Register("crashloop", RestartUnlessStopped, 0)
	// Explicitly stopped app: policy present but user stopped it, so it will not
	// be restarted and must not read as crash-looping.
	m.Register("stopped-by-user", RestartUnlessStopped, 0)
	m.MarkExplicitStop("stopped-by-user")

	m.mu.Lock()
	m.states["crashloop"].FailureCount = 40
	m.mu.Unlock()

	statuses := m.RestartStatuses()

	crash, ok := statuses["crashloop"]
	if !ok {
		t.Fatal("crashloop missing from RestartStatuses")
	}
	if crash.FailureCount != 40 {
		t.Errorf("crashloop FailureCount = %d, want 40", crash.FailureCount)
	}
	if !crash.WillRestart {
		t.Error("crashloop WillRestart = false, want true")
	}

	stopped, ok := statuses["stopped-by-user"]
	if !ok {
		t.Fatal("stopped-by-user missing from RestartStatuses")
	}
	if stopped.WillRestart {
		t.Error("stopped-by-user WillRestart = true, want false (explicit stop)")
	}
}
