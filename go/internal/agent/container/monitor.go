// Package container implements container health monitoring and restart policies.
package container

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/agent/services"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// RestartPolicy determines the container restart behavior.
type RestartPolicy int

const (
	// RestartNo never restarts the container.
	RestartNo RestartPolicy = iota
	// RestartUnlessStopped restarts unless explicitly stopped.
	RestartUnlessStopped
	// RestartOnFailure restarts only on non-zero exit codes.
	RestartOnFailure
	// RestartAlways always restarts the container.
	RestartAlways
)

func (p RestartPolicy) String() string {
	switch p {
	case RestartNo:
		return "no"
	case RestartUnlessStopped:
		return "unless-stopped"
	case RestartOnFailure:
		return "on-failure"
	case RestartAlways:
		return "always"
	default:
		return fmt.Sprintf("unknown(%d)", int(p))
	}
}

// ParseRestartPolicy converts a string to a RestartPolicy.
func ParseRestartPolicy(s string) (RestartPolicy, error) {
	switch s {
	case "no", "":
		return RestartNo, nil
	case "unless-stopped":
		return RestartUnlessStopped, nil
	case "on-failure":
		return RestartOnFailure, nil
	case "always":
		return RestartAlways, nil
	default:
		return RestartNo, fmt.Errorf("unknown restart policy: %q", s)
	}
}

// containerState tracks the runtime state of a monitored container.
type containerState struct {
	FailureCount  int
	LastRestart   time.Time
	ExplicitStop  bool
	RestartPolicy RestartPolicy
	MaxRetries    int
}

// ContainerMonitor monitors container health and implements restart policies.
type ContainerMonitor struct {
	logger     *zap.Logger
	containerd services.ContainerdClient
	logManager *services.ContainerLogManager
	states     map[string]*containerState
	// groupRestarting tracks shared-namespace app groups with an in-flight group
	// restart, keyed by appID. A group restart stops every member, so a later
	// tick would otherwise see the siblings stopped and launch a second,
	// overlapping restart that races on the primary PID. Guarded by mu.
	groupRestarting map[string]bool
	mu              sync.Mutex
	interval        time.Duration
}

func NewContainerMonitor(logger *zap.Logger, client services.ContainerdClient, logManager *services.ContainerLogManager, interval time.Duration) *ContainerMonitor {
	if interval == 0 {
		interval = 5 * time.Second
	}
	return &ContainerMonitor{
		logger:          logger,
		containerd:      client,
		logManager:      logManager,
		states:          make(map[string]*containerState),
		groupRestarting: make(map[string]bool),
		interval:        interval,
	}
}

// Register registers a container for monitoring with a given restart policy.
func (m *ContainerMonitor) Register(appName string, policy RestartPolicy, maxRetries int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.states[appName] = &containerState{
		RestartPolicy: policy,
		MaxRetries:    maxRetries,
	}
	m.logger.Info("Container registered for monitoring",
		zap.String("app_name", appName),
		zap.Int("policy", int(policy)),
	)
}

// Unregister removes a container from monitoring.
func (m *ContainerMonitor) Unregister(appName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.states, appName)
}

// MarkExplicitStop marks a container as explicitly stopped, preventing restart.
func (m *ContainerMonitor) MarkExplicitStop(appName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.states[appName]; ok {
		state.ExplicitStop = true
	}
}

// ClearExplicitStop reverts a prior MarkExplicitStop, re-enabling automatic
// restarts for the container. It is a no-op if appName is not registered.
func (m *ContainerMonitor) ClearExplicitStop(appName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state, ok := m.states[appName]; ok {
		state.ExplicitStop = false
	}
}

// ReconcileBootContainers brings apps back after a device boot. containerd
// keeps container definitions across a reboot but loses their tasks, so without
// this every app sits stopped until manually started. It registers each
// container whose restart policy keeps it running (and that the user didn't
// explicitly stop) under that policy, then runs one immediate reconcile so the
// stopped ones are (re)launched without waiting for the next tick. Intended to
// be called once at agent startup.
//
// Apps deployed with the default policy (unless-stopped) come back; apps
// deployed with --no-restart, and apps the user explicitly stopped, stay down.
func (m *ContainerMonitor) ReconcileBootContainers(ctx context.Context) {
	// One-time upgrade back-fill: apps stopped under an older agent carry no
	// stopped-by-user mark, so without this the first post-upgrade boot would
	// resurrect them. Runs once (persistent marker); must precede the listing
	// below so the marks are in place before we decide what to start.
	if err := m.containerd.MigrateStoppedByUserOnce(ctx); err != nil {
		m.logger.Warn("Boot reconcile migration failed; proceeding without it", zap.Error(err))
	}

	bcs, err := m.containerd.ListBootContainers(ctx)
	if err != nil {
		m.logger.Error("Failed to list boot containers", zap.Error(err))
		return
	}
	if len(bcs) == 0 {
		return
	}
	for _, bc := range bcs {
		// Empty policy means "default keep-running"; map it to unless-stopped.
		policy := RestartUnlessStopped
		if bc.RestartPolicy != "" {
			p, parseErr := ParseRestartPolicy(bc.RestartPolicy)
			if parseErr != nil {
				m.logger.Warn("Skipping boot container with unparseable restart policy",
					zap.String("app_name", bc.Name), zap.String("policy", bc.RestartPolicy), zap.Error(parseErr))
				continue
			}
			policy = p
		}
		m.Register(bc.Name, policy, bc.MaxRetries)
	}
	m.logger.Info("Reconciling apps on boot", zap.Int("count", len(bcs)))
	// Immediate pass: start the ones that aren't running yet (the common
	// post-reboot case) instead of waiting up to one tick interval.
	m.checkContainers(ctx)
}

// Run is the main monitoring loop that checks container health and restarts as needed.
func (m *ContainerMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkContainers(ctx)
		}
	}
}

// checkContainers queries containerd for running containers and restarts any that
// have exited according to their restart policy.
func (m *ContainerMonitor) checkContainers(ctx context.Context) {
	containers, err := m.containerd.ListContainers(ctx)
	if err != nil {
		m.logger.Error("Failed to list containers for health check", zap.Error(err))
		return
	}

	toRestart := m.planRestarts(containers)

	for _, act := range m.planRestartActions(ctx, toRestart) {
		if act.groupAppID != "" {
			go m.restartGroup(ctx, act.groupAppID)
		} else {
			go m.restartSingle(ctx, act.single)
		}
	}
}

// restartAction is one unit of restart work: either a single container, or an
// entire shared-namespace app group identified by appID.
type restartAction struct {
	single     string // restart this container on its own
	groupAppID string // restart this shared-namespace group as a unit
}

// planRestartActions maps the flat list of containers due for restart into
// restart units, collapsing members of the same shared-namespace group into a
// single group restart. Members of a shared-namespace group must restart
// together: a secondary's namespace join is resolved against the primary's live
// task, so restarting members independently would leave a secondary attached to
// a dead namespace.
func (m *ContainerMonitor) planRestartActions(ctx context.Context, toRestart []string) []restartAction {
	gr, _ := m.containerd.(services.GroupRestarter)
	seenGroup := make(map[string]bool)
	var actions []restartAction
	for _, name := range toRestart {
		if gr != nil {
			if appID, grouped := gr.GroupRestartAppID(ctx, name); grouped {
				if seenGroup[appID] {
					continue
				}
				seenGroup[appID] = true
				actions = append(actions, restartAction{groupAppID: appID})
				continue
			}
		}
		actions = append(actions, restartAction{single: name})
	}
	return actions
}

// restartSingle restarts one container and drains its output to the log manager.
func (m *ContainerMonitor) restartSingle(ctx context.Context, name string) {
	outputCh, err := m.containerd.StartContainer(ctx, name, "", nil)
	if err != nil {
		m.logger.Error("Failed to restart container",
			zap.String("app_name", name),
			zap.Error(err),
		)
		return
	}
	m.drainOutput(name, outputCh)
}

// restartGroup restarts an entire shared-namespace app group as a unit, draining
// each member's output to the log manager.
func (m *ContainerMonitor) restartGroup(ctx context.Context, appID string) {
	gr, ok := m.containerd.(services.GroupRestarter)
	if !ok {
		// Should not happen: only reachable when planRestartActions produced a
		// group action, which requires the client to be a GroupRestarter.
		m.logger.Error("group restart requested but client is not a GroupRestarter",
			zap.String("app_id", appID))
		return
	}

	// Guard against overlapping restarts of the same group: a group restart
	// stops every member, so a later tick can see the siblings stopped and try
	// to restart the group again while this one is still in flight.
	m.mu.Lock()
	if m.groupRestarting[appID] {
		m.mu.Unlock()
		return
	}
	m.groupRestarting[appID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.groupRestarting, appID)
		m.mu.Unlock()
	}()
	channels, err := gr.RestartGroup(ctx, appID)
	// RestartGroup can return partially-started services together with an
	// error (e.g. the primary started but a secondary failed). Drain every
	// returned channel even on error: an abandoned channel back-pressures
	// through the agent's pipes into the service's stdout FIFO and freezes
	// the process in pipe_write once the buffers fill (WDY-1822).
	for name, ch := range channels {
		go m.drainOutput(name, ch)
	}
	if err != nil {
		m.logger.Error("Failed to restart app group",
			zap.String("app_id", appID),
			zap.Error(err),
		)
	}
}

// drainOutput consumes a container's output channel so the containerd pipe never
// blocks, publishing through the log manager when available so stdout/stderr
// from restarted containers reaches OTel (and therefore `wendy device logs`).
func (m *ContainerMonitor) drainOutput(name string, outputCh <-chan services.ContainerOutput) {
	for output := range outputCh {
		if m.logManager != nil {
			m.logManager.Publish(name, output)
		}
	}
	if m.logManager != nil {
		m.logManager.Publish(name, services.ContainerOutput{Done: true})
	}
}

// planRestarts reconciles the registered container states against the current
// container list and returns the names of containers that should be restarted,
// advancing their FailureCount/LastRestart as a side effect.
func (m *ContainerMonitor) planRestarts(containers []*agentpb.AppContainer) []string {
	// Build the set of running container identities, keyed the same way the
	// monitor registers state. Services-map apps are monitored per service under
	// the "{appID}_{serviceName}" container name (see containerd.ContainerName /
	// AppConfig.ContainerName), so key each service by that name using its own
	// running state. Apps with no services (legacy single-container apps) are
	// monitored under the bare appID. Keying only by bare appID — as before —
	// meant running["{appID}_{serviceName}"] was never true, so the monitor
	// force-restarted healthy services-map apps every tick (WDY-1552).
	running := make(map[string]bool)
	for _, c := range containers {
		svcs := c.GetServices()
		if len(svcs) == 0 {
			if c.GetRunningState() == agentpb.AppRunningState_RUNNING {
				running[c.GetAppName()] = true
			}
			continue
		}
		for _, s := range svcs {
			if s.GetRunningState() != agentpb.AppRunningState_RUNNING {
				continue
			}
			if s.GetName() == "" {
				// Defensive: a serviceless entry maps to the bare appID.
				running[c.GetAppName()] = true
				continue
			}
			// Keep in sync with containerd.ContainerName.
			running[c.GetAppName()+"_"+s.GetName()] = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var toRestart []string
	for appName, state := range m.states {
		if running[appName] {
			continue
		}
		if !m.shouldRestart(state) {
			continue
		}
		if time.Since(state.LastRestart) < 10*time.Second {
			continue
		}
		m.logger.Info("Restarting container",
			zap.String("app_name", appName),
			zap.Int("failure_count", state.FailureCount),
		)
		state.FailureCount++
		state.LastRestart = time.Now()
		toRestart = append(toRestart, appName)
	}
	return toRestart
}

// shouldRestart determines whether a container should be restarted based on its policy.
func (m *ContainerMonitor) shouldRestart(state *containerState) bool {
	switch state.RestartPolicy {
	case RestartNo:
		return false
	case RestartUnlessStopped:
		return !state.ExplicitStop
	case RestartOnFailure:
		// The monitor detects only whether a container has stopped; it has no
		// exit-code signal from containerd. Until exit-code detection is added,
		// ON_FAILURE behaves like UNLESS_STOPPED: it restarts on any exit, not
		// only non-zero ones. MaxRetries is still enforced.
		if state.ExplicitStop {
			return false
		}
		if state.MaxRetries > 0 && state.FailureCount >= state.MaxRetries {
			return false
		}
		return true
	case RestartAlways:
		return true
	default:
		return false
	}
}
