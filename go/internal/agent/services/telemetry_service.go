package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

const (
	defaultMaxCachedLogs = 20
	// maxCachedResourcesPerService caps how many distinct ResourceMetrics entries
	// (e.g. individual pods / containers) are retained in the per-service metrics
	// cache. Without this cap, high-churn resource attributes (like container IDs
	// that change on every restart) would grow the cache without bound.
	// Oldest entries are evicted first (FIFO) when the cap is reached.
	maxCachedResourcesPerService = 100
)

type TelemetryBroadcaster struct {
	mu            sync.RWMutex
	logSubs       map[string]chan *otelpb.ExportLogsServiceRequest
	metricSubs    map[string]chan *otelpb.ExportMetricsServiceRequest
	traceSubs     map[string]chan *otelpb.ExportTraceServiceRequest
	nextID        uint64
	recentLogs    [defaultMaxCachedLogs]*otelpb.ExportLogsServiceRequest
	logHead       int                                            // next write index (0..defaultMaxCachedLogs-1)
	logCount      int                                            // number of valid entries (0..defaultMaxCachedLogs)
	latestMetrics map[string]*otelpb.ExportMetricsServiceRequest // keyed by "service"
}

func NewTelemetryBroadcaster() *TelemetryBroadcaster {
	return &TelemetryBroadcaster{
		logSubs:       make(map[string]chan *otelpb.ExportLogsServiceRequest),
		metricSubs:    make(map[string]chan *otelpb.ExportMetricsServiceRequest),
		traceSubs:     make(map[string]chan *otelpb.ExportTraceServiceRequest),
		latestMetrics: make(map[string]*otelpb.ExportMetricsServiceRequest),
	}
}

func (b *TelemetryBroadcaster) nextSubID() string {
	b.nextID++
	return fmt.Sprintf("sub-%d", b.nextID)
}

// SubscribeLogs adds a log subscriber and returns the channel and subscription ID.
// Cached recent logs are pre-filled into the channel synchronously before returning.
func (b *TelemetryBroadcaster) SubscribeLogs() (string, <-chan *otelpb.ExportLogsServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID()
	ch := make(chan *otelpb.ExportLogsServiceRequest, 64)
	b.logSubs[id] = ch

	// Pre-fill cached logs synchronously while the lock is held. The 64-slot
	// buffer is always larger than defaultMaxCachedLogs (20), so these sends
	// never block and eliminate the data race between a background goroutine
	// sending on ch and UnsubscribeLogs closing it.
	if b.logCount > 0 {
		start := (b.logHead - b.logCount + defaultMaxCachedLogs) % defaultMaxCachedLogs
		for i := 0; i < b.logCount; i++ {
			ch <- b.recentLogs[(start+i)%defaultMaxCachedLogs]
		}
	}

	return id, ch
}

// SubscribeLogsNoPrefill adds a log subscriber without pre-filling cached logs.
// Use this when the caller will replay disk history itself (via ReadLastN) to
// avoid sending the same recent batches twice — once from disk and again from
// the in-memory cache. Live telemetry published after this call is buffered in
// the returned channel and will be delivered after the caller's history replay.
func (b *TelemetryBroadcaster) SubscribeLogsNoPrefill() (string, <-chan *otelpb.ExportLogsServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID()
	ch := make(chan *otelpb.ExportLogsServiceRequest, 64)
	b.logSubs[id] = ch
	return id, ch
}

// SubscribeMetricsNoPrefill adds a metrics subscriber without pre-filling cached snapshots.
// Use when the caller replays disk history to avoid duplicate metric deliveries.
func (b *TelemetryBroadcaster) SubscribeMetricsNoPrefill() (string, <-chan *otelpb.ExportMetricsServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID()
	ch := make(chan *otelpb.ExportMetricsServiceRequest, 64)
	b.metricSubs[id] = ch
	return id, ch
}

// UnsubscribeLogs removes a log subscriber.
func (b *TelemetryBroadcaster) UnsubscribeLogs(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.logSubs[id]; ok {
		close(ch)
		delete(b.logSubs, id)
	}
}

func (b *TelemetryBroadcaster) PublishLogs(req *otelpb.ExportLogsServiceRequest) {
	b.mu.Lock()
	b.recentLogs[b.logHead] = req
	b.logHead = (b.logHead + 1) % defaultMaxCachedLogs
	if b.logCount < defaultMaxCachedLogs {
		b.logCount++
	}
	for _, ch := range b.logSubs {
		select {
		case ch <- req:
		default:
			// Drop if subscriber is slow.
		}
	}
	b.mu.Unlock()
}

// SubscribeMetrics adds a metrics subscriber.
// Cached latest metrics are pre-filled into the channel synchronously before returning.
func (b *TelemetryBroadcaster) SubscribeMetrics() (string, <-chan *otelpb.ExportMetricsServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID()
	ch := make(chan *otelpb.ExportMetricsServiceRequest, 64)
	b.metricSubs[id] = ch

	// Pre-fill one snapshot per service synchronously. Clones are made while
	// the lock is held so concurrent PublishMetrics cannot mutate the cached
	// object after it is enqueued. Non-blocking sends guard against the
	// unlikely case where the number of distinct service names exceeds 64.
	if len(b.latestMetrics) > 0 {
		seen := make(map[*otelpb.ExportMetricsServiceRequest]bool, len(b.latestMetrics))
		for _, v := range b.latestMetrics {
			if !seen[v] {
				seen[v] = true
				select {
				case ch <- proto.Clone(v).(*otelpb.ExportMetricsServiceRequest):
				default:
				}
			}
		}
	}

	return id, ch
}

func (b *TelemetryBroadcaster) UnsubscribeMetrics(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.metricSubs[id]; ok {
		close(ch)
		delete(b.metricSubs, id)
	}
}

func (b *TelemetryBroadcaster) PublishMetrics(req *otelpb.ExportMetricsServiceRequest) {
	b.mu.Lock()
	for _, rm := range req.GetResourceMetrics() {
		serviceName := resourceServiceName(rm.GetResource())
		b.latestMetrics[serviceName] = mergeServiceMetrics(b.latestMetrics[serviceName], rm)
	}
	for _, ch := range b.metricSubs {
		select {
		case ch <- req:
		default:
		}
	}
	b.mu.Unlock()
}

// mergeServiceMetrics upserts the metrics in rm into the cached per-service
// request, keyed by resource identity, scope identity, and metric name.
// Metrics absent from the new batch are retained so partial batches do not
// drop previously reported metrics for late subscribers. Multiple resource
// instances sharing the same service.name (e.g. different pods) are kept as
// separate ResourceMetrics entries distinguished by their resource attributes.
func mergeServiceMetrics(cached *otelpb.ExportMetricsServiceRequest, rm *otelpb.ResourceMetrics) *otelpb.ExportMetricsServiceRequest {
	// Clone rm so the cache never holds references to live-broadcast request objects.
	// Without this, a subscriber that has queued a broadcast req could observe mutations
	// to its ResourceMetrics objects the next time the same service publishes a batch.
	rm = proto.Clone(rm).(*otelpb.ResourceMetrics)
	if cached == nil || len(cached.GetResourceMetrics()) == 0 {
		return &otelpb.ExportMetricsServiceRequest{
			ResourceMetrics: []*otelpb.ResourceMetrics{rm},
		}
	}

	// Find the cached ResourceMetrics with matching resource identity. Different
	// instances of the same service (e.g. pods) share service.name but differ in
	// other attributes (e.g. service.instance.id); keying by full resource identity
	// preserves each instance's metrics as a separate entry.
	rmKey := resourceKey(rm.GetResource())
	var dst *otelpb.ResourceMetrics
	for _, existing := range cached.GetResourceMetrics() {
		if resourceKey(existing.GetResource()) == rmKey {
			dst = existing
			break
		}
	}
	if dst == nil {
		cached.ResourceMetrics = append(cached.ResourceMetrics, rm)
		// Evict oldest entries once the per-service cap is reached so that
		// high-churn resource attributes (e.g. unique container IDs per restart)
		// cannot grow the cache without bound.
		if len(cached.ResourceMetrics) > maxCachedResourcesPerService {
			cached.ResourceMetrics = cached.ResourceMetrics[len(cached.ResourceMetrics)-maxCachedResourcesPerService:]
		}
		return cached
	}

	dst.Resource = rm.GetResource()
	dst.SchemaUrl = rm.GetSchemaUrl()

	// Index by full scope identity (name + version + schema_url) to avoid
	// conflating scopes that share a name but differ in version or schema.
	scopeIdx := make(map[string]*otelpb.ScopeMetrics, len(dst.GetScopeMetrics()))
	for _, sm := range dst.GetScopeMetrics() {
		scopeIdx[scopeKey(sm)] = sm
	}

	for _, sm := range rm.GetScopeMetrics() {
		key := scopeKey(sm)
		existing, ok := scopeIdx[key]
		if !ok {
			dst.ScopeMetrics = append(dst.ScopeMetrics, sm)
			scopeIdx[key] = sm
			continue
		}
		metricIdx := make(map[string]int, len(existing.GetMetrics()))
		for i, m := range existing.GetMetrics() {
			metricIdx[m.GetName()] = i
		}
		for _, m := range sm.GetMetrics() {
			if i, ok := metricIdx[m.GetName()]; ok {
				existing.Metrics[i] = m
			} else {
				existing.Metrics = append(existing.Metrics, m)
				metricIdx[m.GetName()] = len(existing.Metrics) - 1
			}
		}
	}
	return cached
}

func resourceKey(r *otelpb.Resource) string {
	attrs := r.GetAttributes()
	if len(attrs) == 0 {
		return ""
	}
	parts := make([]string, len(attrs))
	for i, kv := range attrs {
		parts[i] = kv.GetKey() + "=" + kv.GetValue().String()
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

func scopeKey(sm *otelpb.ScopeMetrics) string {
	return sm.GetScope().GetName() + "\x00" + sm.GetScope().GetVersion() + "\x00" + sm.GetSchemaUrl()
}

func (b *TelemetryBroadcaster) SubscribeTraces() (string, <-chan *otelpb.ExportTraceServiceRequest) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSubID()
	ch := make(chan *otelpb.ExportTraceServiceRequest, 64)
	b.traceSubs[id] = ch
	return id, ch
}

func (b *TelemetryBroadcaster) UnsubscribeTraces(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.traceSubs[id]; ok {
		close(ch)
		delete(b.traceSubs, id)
	}
}

func (b *TelemetryBroadcaster) PublishTraces(req *otelpb.ExportTraceServiceRequest) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.traceSubs {
		select {
		case ch <- req:
		default:
		}
	}
}

type TelemetryService struct {
	agentpb.UnimplementedWendyTelemetryServiceServer
	logger      *zap.Logger
	broadcaster *TelemetryBroadcaster
	buffer      *TelemetryBuffer // nil if disk buffering is unavailable
}

// NewTelemetryService creates a new TelemetryService.
func NewTelemetryService(logger *zap.Logger, broadcaster *TelemetryBroadcaster, buffer *TelemetryBuffer) *TelemetryService {
	return &TelemetryService{
		logger:      logger,
		broadcaster: broadcaster,
		buffer:      buffer,
	}
}

func (s *TelemetryService) Broadcaster() *TelemetryBroadcaster {
	return s.broadcaster
}

func (s *TelemetryService) StreamLogs(req *agentpb.StreamLogsRequest, stream grpc.ServerStreamingServer[agentpb.StreamLogsResponse]) error {
	ctx := stream.Context()

	// When replaying history, subscribe without cache prefill first so that
	// live telemetry published during replay is buffered rather than lost,
	// and to avoid sending the same recent batches twice (once from disk and
	// once from the broadcaster's in-memory ring buffer).
	var id string
	var ch <-chan *otelpb.ExportLogsServiceRequest
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		id, ch = s.broadcaster.SubscribeLogsNoPrefill()
	} else {
		id, ch = s.broadcaster.SubscribeLogs()
	}
	defer s.broadcaster.UnsubscribeLogs(id)

	// Replay history if requested (after subscribing so no live items are missed).
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		// Count the tail window against batches that survive the filter: the
		// last N batches device-wide may all belong to a chatty co-tenant,
		// which would replay nothing for the requested app.
		entries := s.buffer.ReadLastNMatching(SignalLogs, int(*req.LastN), func(m proto.Message) proto.Message {
			logs, ok := m.(*otelpb.ExportLogsServiceRequest)
			if !ok {
				return nil
			}
			if req.AppName != nil || req.ServiceName != nil || req.MinSeverity != nil {
				if logs = filterLogs(logs, req); logs == nil {
					return nil
				}
			}
			return logs
		})
		for _, e := range entries {
			logs, ok := e.(*otelpb.ExportLogsServiceRequest)
			if !ok {
				continue
			}
			if err := stream.Send(&agentpb.StreamLogsResponse{Logs: logs, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	s.logger.Info("StreamLogs client connected", zap.String("sub_id", id))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case logReq, ok := <-ch:
			if !ok {
				return nil
			}

			// Apply filters if requested.
			if req.AppName != nil || req.ServiceName != nil || req.MinSeverity != nil {
				logReq = filterLogs(logReq, req)
				if logReq == nil {
					continue
				}
			}

			if err := stream.Send(&agentpb.StreamLogsResponse{
				Logs: logReq,
			}); err != nil {
				return err
			}
		}
	}
}

func (s *TelemetryService) StreamMetrics(req *agentpb.StreamMetricsRequest, stream grpc.ServerStreamingServer[agentpb.StreamMetricsResponse]) error {
	ctx := stream.Context()

	// Subscribe first (without cache prefill when replaying history) so that
	// live telemetry is buffered during the replay window and not lost.
	var id string
	var ch <-chan *otelpb.ExportMetricsServiceRequest
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		id, ch = s.broadcaster.SubscribeMetricsNoPrefill()
	} else {
		id, ch = s.broadcaster.SubscribeMetrics()
	}
	defer s.broadcaster.UnsubscribeMetrics(id)

	// Replay history after subscribing. As with logs, the tail window counts
	// batches that survive the filter, not the last N device-wide.
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		entries := s.buffer.ReadLastNMatching(SignalMetrics, int(*req.LastN), func(m proto.Message) proto.Message {
			metrics, ok := m.(*otelpb.ExportMetricsServiceRequest)
			if !ok {
				return nil
			}
			if req.ServiceName != nil || req.AppName != nil || req.MetricNamePrefix != nil {
				if metrics = filterMetrics(metrics, req); metrics == nil {
					return nil
				}
			}
			return metrics
		})
		for _, e := range entries {
			metrics, ok := e.(*otelpb.ExportMetricsServiceRequest)
			if !ok {
				continue
			}
			if err := stream.Send(&agentpb.StreamMetricsResponse{Metrics: metrics, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	s.logger.Info("StreamMetrics client connected", zap.String("sub_id", id))

	hasFilter := req.ServiceName != nil || req.AppName != nil || req.MetricNamePrefix != nil

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case metricsReq, ok := <-ch:
			if !ok {
				return nil
			}

			if hasFilter {
				metricsReq = filterMetrics(metricsReq, req)
				if metricsReq == nil {
					continue
				}
			}

			if err := stream.Send(&agentpb.StreamMetricsResponse{
				Metrics: metricsReq,
			}); err != nil {
				return err
			}
		}
	}
}

func (s *TelemetryService) StreamTraces(req *agentpb.StreamTracesRequest, stream grpc.ServerStreamingServer[agentpb.StreamTracesResponse]) error {
	ctx := stream.Context()

	// Subscribe first so live traces published during history replay are buffered.
	// Traces have no in-memory cache prefill so SubscribeTraces is always correct here.
	id, ch := s.broadcaster.SubscribeTraces()
	defer s.broadcaster.UnsubscribeTraces(id)

	// Replay history after subscribing. As with logs, the tail window counts
	// batches that survive the filter, not the last N device-wide.
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		entries := s.buffer.ReadLastNMatching(SignalTraces, int(*req.LastN), func(m proto.Message) proto.Message {
			traces, ok := m.(*otelpb.ExportTraceServiceRequest)
			if !ok {
				return nil
			}
			if req.ServiceName != nil || req.AppName != nil || req.SpanNamePrefix != nil {
				if traces = filterTraces(traces, req); traces == nil {
					return nil
				}
			}
			return traces
		})
		for _, e := range entries {
			traces, ok := e.(*otelpb.ExportTraceServiceRequest)
			if !ok {
				continue
			}
			if err := stream.Send(&agentpb.StreamTracesResponse{Traces: traces, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	s.logger.Info("StreamTraces client connected", zap.String("sub_id", id))

	hasFilter := req.ServiceName != nil || req.AppName != nil || req.SpanNamePrefix != nil

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case traceReq, ok := <-ch:
			if !ok {
				return nil
			}

			if hasFilter {
				traceReq = filterTraces(traceReq, req)
				if traceReq == nil {
					continue
				}
			}

			if err := stream.Send(&agentpb.StreamTracesResponse{
				Traces: traceReq,
			}); err != nil {
				return err
			}
		}
	}
}

type OTELLogsReceiver struct {
	otelpb.UnimplementedLogsServiceServer
	broadcaster TelemetryPublisher
}

// NewOTELLogsReceiver creates a new OTELLogsReceiver.
func NewOTELLogsReceiver(b TelemetryPublisher) *OTELLogsReceiver {
	return &OTELLogsReceiver{broadcaster: b}
}

func (r *OTELLogsReceiver) Export(_ context.Context, req *otelpb.ExportLogsServiceRequest) (*otelpb.ExportLogsServiceResponse, error) {
	r.broadcaster.PublishLogs(req)
	return &otelpb.ExportLogsServiceResponse{}, nil
}

type OTELMetricsReceiver struct {
	otelpb.UnimplementedMetricsServiceServer
	broadcaster TelemetryPublisher
}

// NewOTELMetricsReceiver creates a new OTELMetricsReceiver.
func NewOTELMetricsReceiver(b TelemetryPublisher) *OTELMetricsReceiver {
	return &OTELMetricsReceiver{broadcaster: b}
}

func (r *OTELMetricsReceiver) Export(_ context.Context, req *otelpb.ExportMetricsServiceRequest) (*otelpb.ExportMetricsServiceResponse, error) {
	r.broadcaster.PublishMetrics(req)
	return &otelpb.ExportMetricsServiceResponse{}, nil
}

type OTELTraceReceiver struct {
	otelpb.UnimplementedTraceServiceServer
	broadcaster TelemetryPublisher
}

// NewOTELTraceReceiver creates a new OTELTraceReceiver.
func NewOTELTraceReceiver(b TelemetryPublisher) *OTELTraceReceiver {
	return &OTELTraceReceiver{broadcaster: b}
}

func (r *OTELTraceReceiver) Export(_ context.Context, req *otelpb.ExportTraceServiceRequest) (*otelpb.ExportTraceServiceResponse, error) {
	r.broadcaster.PublishTraces(req)
	return &otelpb.ExportTraceServiceResponse{}, nil
}

func matchResourceAttributes(resource *otelpb.Resource, serviceName *string, appName *string) bool {
	if serviceName == nil && appName == nil {
		return true
	}
	for _, attr := range resource.GetAttributes() {
		if attr.GetKey() == "service.name" {
			val := attr.GetValue().GetStringValue()
			if serviceName != nil && val == *serviceName {
				return true
			}
			if appName != nil && val == *appName {
				return true
			}
			return false
		}
	}
	return false
}

func resourceServiceName(resource *otelpb.Resource) string {
	for _, attr := range resource.GetAttributes() {
		if attr.GetKey() == "service.name" {
			return attr.GetValue().GetStringValue()
		}
	}
	return ""
}

// filterLogs filters log records based on the stream request filters.
// Returns nil if all records are filtered out.
func filterLogs(req *otelpb.ExportLogsServiceRequest, filter *agentpb.StreamLogsRequest) *otelpb.ExportLogsServiceRequest {
	if filter == nil {
		return req
	}

	serviceName := filter.ServiceName
	appName := filter.AppName
	var minSeverity int32
	if filter.MinSeverity != nil {
		minSeverity = *filter.MinSeverity
	}

	// If no filters, pass through.
	if serviceName == nil && appName == nil && minSeverity == 0 {
		return req
	}

	var filteredResourceLogs []*otelpb.ResourceLogs
	for _, rl := range req.GetResourceLogs() {
		// Check resource attributes for service.name.
		if !matchResourceAttributes(rl.GetResource(), serviceName, appName) {
			continue
		}

		// Filter by severity if specified.
		if minSeverity > 0 {
			var filteredScopeLogs []*otelpb.ScopeLogs
			for _, sl := range rl.GetScopeLogs() {
				var filteredRecords []*otelpb.LogRecord
				for _, lr := range sl.GetLogRecords() {
					if int32(lr.GetSeverityNumber()) >= minSeverity {
						filteredRecords = append(filteredRecords, lr)
					}
				}
				if len(filteredRecords) > 0 {
					filtered := &otelpb.ScopeLogs{
						Scope:      sl.GetScope(),
						LogRecords: filteredRecords,
						SchemaUrl:  sl.GetSchemaUrl(),
					}
					filteredScopeLogs = append(filteredScopeLogs, filtered)
				}
			}
			if len(filteredScopeLogs) > 0 {
				filteredResourceLogs = append(filteredResourceLogs, &otelpb.ResourceLogs{
					Resource:  rl.GetResource(),
					ScopeLogs: filteredScopeLogs,
					SchemaUrl: rl.GetSchemaUrl(),
				})
			}
		} else {
			filteredResourceLogs = append(filteredResourceLogs, rl)
		}
	}

	if len(filteredResourceLogs) == 0 {
		return nil
	}
	return &otelpb.ExportLogsServiceRequest{ResourceLogs: filteredResourceLogs}
}

// filterMetrics filters metrics based on the stream request filters.
// Returns nil if all metrics are filtered out.
func filterMetrics(req *otelpb.ExportMetricsServiceRequest, filter *agentpb.StreamMetricsRequest) *otelpb.ExportMetricsServiceRequest {
	if filter == nil {
		return req
	}

	serviceName := filter.ServiceName
	appName := filter.AppName
	metricNamePrefix := filter.MetricNamePrefix

	if serviceName == nil && appName == nil && metricNamePrefix == nil {
		return req
	}

	var filteredResourceMetrics []*otelpb.ResourceMetrics
	for _, rm := range req.GetResourceMetrics() {
		if !matchResourceAttributes(rm.GetResource(), serviceName, appName) {
			continue
		}

		if metricNamePrefix != nil {
			prefix := *metricNamePrefix
			var filteredScopeMetrics []*otelpb.ScopeMetrics
			for _, sm := range rm.GetScopeMetrics() {
				var filteredMetrics []*otelpb.Metric
				for _, m := range sm.GetMetrics() {
					if strings.HasPrefix(m.GetName(), prefix) {
						filteredMetrics = append(filteredMetrics, m)
					}
				}
				if len(filteredMetrics) > 0 {
					filteredScopeMetrics = append(filteredScopeMetrics, &otelpb.ScopeMetrics{
						Scope:     sm.GetScope(),
						Metrics:   filteredMetrics,
						SchemaUrl: sm.GetSchemaUrl(),
					})
				}
			}
			if len(filteredScopeMetrics) > 0 {
				filteredResourceMetrics = append(filteredResourceMetrics, &otelpb.ResourceMetrics{
					Resource:     rm.GetResource(),
					ScopeMetrics: filteredScopeMetrics,
					SchemaUrl:    rm.GetSchemaUrl(),
				})
			}
		} else {
			filteredResourceMetrics = append(filteredResourceMetrics, rm)
		}
	}

	if len(filteredResourceMetrics) == 0 {
		return nil
	}
	return &otelpb.ExportMetricsServiceRequest{ResourceMetrics: filteredResourceMetrics}
}

// filterTraces filters traces based on the stream request filters.
// Returns nil if all spans are filtered out.
func filterTraces(req *otelpb.ExportTraceServiceRequest, filter *agentpb.StreamTracesRequest) *otelpb.ExportTraceServiceRequest {
	if filter == nil {
		return req
	}

	serviceName := filter.ServiceName
	appName := filter.AppName
	spanNamePrefix := filter.SpanNamePrefix

	if serviceName == nil && appName == nil && spanNamePrefix == nil {
		return req
	}

	var filteredResourceSpans []*otelpb.ResourceSpans
	for _, rs := range req.GetResourceSpans() {
		if !matchResourceAttributes(rs.GetResource(), serviceName, appName) {
			continue
		}

		if spanNamePrefix != nil {
			prefix := *spanNamePrefix
			var filteredScopeSpans []*otelpb.ScopeSpans
			for _, ss := range rs.GetScopeSpans() {
				var filteredSpans []*otelpb.Span
				for _, s := range ss.GetSpans() {
					if strings.HasPrefix(s.GetName(), prefix) {
						filteredSpans = append(filteredSpans, s)
					}
				}
				if len(filteredSpans) > 0 {
					filteredScopeSpans = append(filteredScopeSpans, &otelpb.ScopeSpans{
						Scope:     ss.GetScope(),
						Spans:     filteredSpans,
						SchemaUrl: ss.GetSchemaUrl(),
					})
				}
			}
			if len(filteredScopeSpans) > 0 {
				filteredResourceSpans = append(filteredResourceSpans, &otelpb.ResourceSpans{
					Resource:   rs.GetResource(),
					ScopeSpans: filteredScopeSpans,
					SchemaUrl:  rs.GetSchemaUrl(),
				})
			}
		} else {
			filteredResourceSpans = append(filteredResourceSpans, rs)
		}
	}

	if len(filteredResourceSpans) == 0 {
		return nil
	}
	return &otelpb.ExportTraceServiceRequest{ResourceSpans: filteredResourceSpans}
}

// Ensure compile-time interface compliance.
var (
	_ agentpb.WendyTelemetryServiceServer = (*TelemetryService)(nil)
	_ otelpb.LogsServiceServer            = (*OTELLogsReceiver)(nil)
	_ otelpb.MetricsServiceServer         = (*OTELMetricsReceiver)(nil)
	_ otelpb.TraceServiceServer           = (*OTELTraceReceiver)(nil)
)
