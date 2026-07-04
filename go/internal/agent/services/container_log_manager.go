package services

import (
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

type logSubscriber struct {
	mu     sync.Mutex
	ch     chan ContainerOutput
	closed bool
}

// send attempts a non-blocking send to the subscriber.
// It is a no-op if the subscriber is already closed; the mutex ensures this
// check and the channel send cannot race with close.
func (s *logSubscriber) send(output ContainerOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- output:
	default:
		// Drop if subscriber is slow.
	}
}

// close marks the subscriber as closed and closes the underlying channel.
// Safe to call once; subsequent calls are no-ops.
func (s *logSubscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// exitEventScope marks the OTel instrumentation scope of structured
// container-exit telemetry events. Clients (e.g. `wendy device crashes`)
// recognise an exit/crash record by this scope name.
const exitEventScope = "wendy.container.exit"

// Exit-event attributes. Stable strings: clients filter and render on them.
const (
	exitAttrEvent    = "event"         // always exitEventName
	exitAttrCode     = "exit.code"     // process exit code (int)
	exitAttrCrash    = "crash"         // bool: non-zero exit or unobservable exit
	exitAttrObserved = "exit.observed" // bool: false when the wait errored
	exitEventName    = "container.exit"
)

type ContainerLogManager struct {
	logger      *zap.Logger
	broadcaster TelemetryPublisher
	mu          sync.RWMutex
	subscribers map[string]map[string]*logSubscriber // appName -> subID -> subscriber
	nextID      uint64
	resources   map[string]*otelpb.Resource // appName -> pre-built OTel resource (protected by mu)
	// lastCrash retains the most recent crash exit event per app so it survives
	// telemetry-buffer eviction (the shared on-disk buffer is a bounded FIFO a
	// chatty neighbour can roll over). Surfaced first on a history replay via
	// PinnedCrashes so a post-mortem always has the crash record. Protected by mu.
	lastCrash map[string]*otelpb.ExportLogsServiceRequest
}

// NewContainerLogManager creates a new ContainerLogManager.
func NewContainerLogManager(logger *zap.Logger, broadcaster TelemetryPublisher) *ContainerLogManager {
	return &ContainerLogManager{
		logger:      logger,
		broadcaster: broadcaster,
		subscribers: make(map[string]map[string]*logSubscriber),
		resources:   make(map[string]*otelpb.Resource),
		lastCrash:   make(map[string]*otelpb.ExportLogsServiceRequest),
	}
}

// RegisterApp caches the OTel resource for an app so that its stdout/stderr logs
// carry service.namespace, service.version, and service.instance.id.
func (m *ContainerLogManager) RegisterApp(appName, version string) {
	resource := containerResource(appName, version)
	m.mu.Lock()
	m.resources[appName] = resource
	m.mu.Unlock()
}

func (m *ContainerLogManager) Subscribe(appName string) (string, <-chan ContainerOutput) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextID++
	subID := fmt.Sprintf("log-sub-%d", m.nextID)

	if m.subscribers[appName] == nil {
		m.subscribers[appName] = make(map[string]*logSubscriber)
	}

	sub := &logSubscriber{ch: make(chan ContainerOutput, 64)}
	m.subscribers[appName][subID] = sub

	m.logger.Debug("Container log subscriber added",
		zap.String("app_name", appName),
		zap.String("sub_id", subID),
	)

	return subID, sub.ch
}

func (m *ContainerLogManager) Unsubscribe(appName string, subID string) {
	m.mu.Lock()

	appSubs, ok := m.subscribers[appName]
	if !ok {
		m.mu.Unlock()
		return
	}

	sub, exists := appSubs[subID]
	if exists {
		delete(appSubs, subID)
	}
	if len(appSubs) == 0 {
		delete(m.subscribers, appName)
	}

	m.mu.Unlock()

	// Close outside the manager lock so that an in-flight Publish sending to
	// this subscriber's channel can acquire sub.mu without deadlocking.
	if exists {
		sub.close()
	}

	m.logger.Debug("Container log subscriber removed",
		zap.String("app_name", appName),
		zap.String("sub_id", subID),
	)
}

func (m *ContainerLogManager) Publish(appName string, output ContainerOutput) {
	m.publishToTelemetry(appName, output)

	// Fan out to all subscribers.
	m.mu.RLock()
	appSubs := m.subscribers[appName]
	for _, sub := range appSubs {
		sub.send(output)
	}
	m.mu.RUnlock()
}

// publishToTelemetry converts container output into OTEL log records and
// publishes them via the TelemetryBroadcaster.
func (m *ContainerLogManager) publishToTelemetry(appName string, output ContainerOutput) {
	if output.Done {
		if output.Exit != nil {
			m.publishExitEvent(appName, output.Exit)
		}
		return
	}

	now := uint64(time.Now().UnixNano())
	var records []*otelpb.LogRecord

	if len(output.Stdout) > 0 {
		records = append(records, &otelpb.LogRecord{
			TimeUnixNano:         now,
			ObservedTimeUnixNano: now,
			SeverityNumber:       otelpb.SeverityNumber_SEVERITY_NUMBER_INFO,
			SeverityText:         "INFO",
			Body: &otelpb.AnyValue{
				Value: &otelpb.AnyValue_StringValue{
					StringValue: string(output.Stdout),
				},
			},
			Attributes: []*otelpb.KeyValue{
				{
					Key: "stream",
					Value: &otelpb.AnyValue{
						Value: &otelpb.AnyValue_StringValue{StringValue: "stdout"},
					},
				},
			},
		})
	}

	if len(output.Stderr) > 0 {
		records = append(records, &otelpb.LogRecord{
			TimeUnixNano:         now,
			ObservedTimeUnixNano: now,
			SeverityNumber:       otelpb.SeverityNumber_SEVERITY_NUMBER_WARN,
			SeverityText:         "WARN",
			Body: &otelpb.AnyValue{
				Value: &otelpb.AnyValue_StringValue{
					StringValue: string(output.Stderr),
				},
			},
			Attributes: []*otelpb.KeyValue{
				{
					Key: "stream",
					Value: &otelpb.AnyValue{
						Value: &otelpb.AnyValue_StringValue{StringValue: "stderr"},
					},
				},
			},
		})
	}

	if len(records) == 0 {
		return
	}

	m.mu.RLock()
	resource := m.resources[appName]
	m.mu.RUnlock()
	if resource == nil {
		resource = containerResource(appName, "")
	}

	m.broadcaster.PublishLogs(&otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{
				Resource: resource,
				ScopeLogs: []*otelpb.ScopeLogs{
					{
						Scope:      &otelpb.InstrumentationScope{Name: "wendy.container"},
						LogRecords: records,
					},
				},
			},
		},
	})
}

// resourceFor returns the cached OTel resource for appName, or a freshly built
// one when the app was never registered.
func (m *ContainerLogManager) resourceFor(appName string) *otelpb.Resource {
	m.mu.RLock()
	resource := m.resources[appName]
	m.mu.RUnlock()
	if resource == nil {
		resource = containerResource(appName, "")
	}
	return resource
}

// publishExitEvent emits a structured "container exit" OTEL log record so a
// stopped or crashed container leaves a serviceable, queryable record in the
// same telemetry buffer as its stdout/stderr. A crash (non-zero or unobservable
// exit) is logged at ERROR and pinned per app so it survives buffer eviction.
func (m *ContainerLogManager) publishExitEvent(appName string, exit *ContainerExit) {
	crash := exit.IsCrash()

	severity := otelpb.SeverityNumber_SEVERITY_NUMBER_INFO
	severityText := "INFO"
	var body string
	switch {
	case exit.WaitErr:
		severity, severityText = otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
		body = fmt.Sprintf("container %q exited (exit status could not be observed)", appName)
	case crash:
		severity, severityText = otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
		body = fmt.Sprintf("container %q crashed with exit code %d", appName, exit.Code)
	default:
		body = fmt.Sprintf("container %q exited cleanly (exit code 0)", appName)
	}

	now := uint64(time.Now().UnixNano())
	attrs := []*otelpb.KeyValue{
		stringKV(exitAttrEvent, exitEventName),
		{Key: exitAttrCrash, Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_BoolValue{BoolValue: crash}}},
		{Key: exitAttrObserved, Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_BoolValue{BoolValue: !exit.WaitErr}}},
	}
	if !exit.WaitErr {
		attrs = append(attrs, &otelpb.KeyValue{
			Key:   exitAttrCode,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: int64(exit.Code)}},
		})
	}

	req := &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{
				Resource: m.resourceFor(appName),
				ScopeLogs: []*otelpb.ScopeLogs{
					{
						Scope: &otelpb.InstrumentationScope{Name: exitEventScope},
						LogRecords: []*otelpb.LogRecord{
							{
								TimeUnixNano:         now,
								ObservedTimeUnixNano: now,
								SeverityNumber:       severity,
								SeverityText:         severityText,
								Body:                 &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
								Attributes:           attrs,
							},
						},
					},
				},
			},
		},
	}

	// Pin crashes so they outlive telemetry-buffer eviction (P2.9). Clean exits
	// are not pinned: they are not post-mortem material and would mask an earlier
	// crash that has not yet been viewed.
	if crash {
		m.mu.Lock()
		m.lastCrash[appName] = req
		m.mu.Unlock()
	}

	m.broadcaster.PublishLogs(req)
}

// PinnedCrashes returns a copy of the most-recent retained crash exit event per
// app, in no particular order. The telemetry service prepends these to a
// history replay so an evicted crash still surfaces to `wendy device crashes`
// and `wendy device logs --tail`.
func (m *ContainerLogManager) PinnedCrashes() []*otelpb.ExportLogsServiceRequest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.lastCrash) == 0 {
		return nil
	}
	out := make([]*otelpb.ExportLogsServiceRequest, 0, len(m.lastCrash))
	for _, req := range m.lastCrash {
		out = append(out, req)
	}
	return out
}
