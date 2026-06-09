package services

import (
	"strings"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// TelemetryServiceV2 implements agentpbv2.WendyTelemetryServiceServer by
// forwarding telemetry events from the shared TelemetryBroadcaster.
type TelemetryServiceV2 struct {
	agentpbv2.UnimplementedWendyTelemetryServiceServer
	logger      *zap.Logger
	broadcaster *TelemetryBroadcaster
	buffer      *TelemetryBuffer
}

// NewTelemetryServiceV2 creates a new TelemetryServiceV2.
func NewTelemetryServiceV2(logger *zap.Logger, broadcaster *TelemetryBroadcaster, buffer *TelemetryBuffer) *TelemetryServiceV2 {
	return &TelemetryServiceV2{logger: logger, broadcaster: broadcaster, buffer: buffer}
}

func (s *TelemetryServiceV2) StreamLogs(req *agentpbv2.StreamLogsRequest, stream grpc.ServerStreamingServer[agentpbv2.StreamLogsResponse]) error {
	// Subscribe first (without cache prefill when replaying history) so live
	// telemetry is buffered during replay and not lost, and to avoid duplicate
	// deliveries from both the disk history and the in-memory ring buffer.
	var subID string
	var ch <-chan *otelpb.ExportLogsServiceRequest
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		subID, ch = s.broadcaster.SubscribeLogsNoPrefill()
	} else {
		subID, ch = s.broadcaster.SubscribeLogs()
	}
	defer s.broadcaster.UnsubscribeLogs(subID)

	// Replay history after subscribing.
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		entries := s.buffer.ReadLastN(SignalLogs, int(*req.LastN))
		for _, e := range entries {
			logs, ok := e.(*otelpb.ExportLogsServiceRequest)
			if !ok {
				continue
			}
			// Apply filters if set.
			if req.AppName != nil || req.ServiceName != nil || req.MinSeverity != nil {
				logs = filterLogsV2(logs, req)
				if logs == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamLogsResponse{Logs: logs, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case item, ok := <-ch:
			if !ok {
				return nil
			}
			// Apply filters for live streaming too.
			if req.AppName != nil || req.ServiceName != nil || req.MinSeverity != nil {
				item = filterLogsV2(item, req)
				if item == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamLogsResponse{Logs: item}); err != nil {
				return err
			}
		}
	}
}

func (s *TelemetryServiceV2) StreamMetrics(req *agentpbv2.StreamMetricsRequest, stream grpc.ServerStreamingServer[agentpbv2.StreamMetricsResponse]) error {
	// Subscribe first to buffer live items during replay; skip cache prefill
	// when replaying history to avoid duplicate metric deliveries.
	var subID string
	var ch <-chan *otelpb.ExportMetricsServiceRequest
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		subID, ch = s.broadcaster.SubscribeMetricsNoPrefill()
	} else {
		subID, ch = s.broadcaster.SubscribeMetrics()
	}
	defer s.broadcaster.UnsubscribeMetrics(subID)

	// Replay history after subscribing.
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		entries := s.buffer.ReadLastN(SignalMetrics, int(*req.LastN))
		for _, e := range entries {
			metrics, ok := e.(*otelpb.ExportMetricsServiceRequest)
			if !ok {
				continue
			}
			if req.ServiceName != nil || req.AppName != nil || req.MetricNamePrefix != nil {
				metrics = filterMetricsV2(metrics, req)
				if metrics == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamMetricsResponse{Metrics: metrics, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case item, ok := <-ch:
			if !ok {
				return nil
			}
			if req.ServiceName != nil || req.AppName != nil || req.MetricNamePrefix != nil {
				item = filterMetricsV2(item, req)
				if item == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamMetricsResponse{Metrics: item}); err != nil {
				return err
			}
		}
	}
}

func (s *TelemetryServiceV2) StreamTraces(req *agentpbv2.StreamTracesRequest, stream grpc.ServerStreamingServer[agentpbv2.StreamTracesResponse]) error {
	// Subscribe first so live traces are buffered during history replay.
	// Traces have no in-memory cache prefill, so SubscribeTraces is always correct.
	subID, ch := s.broadcaster.SubscribeTraces()
	defer s.broadcaster.UnsubscribeTraces(subID)

	// Replay history after subscribing.
	if req.LastN != nil && *req.LastN > 0 && s.buffer != nil && s.buffer.DiskEnabled() {
		entries := s.buffer.ReadLastN(SignalTraces, int(*req.LastN))
		for _, e := range entries {
			traces, ok := e.(*otelpb.ExportTraceServiceRequest)
			if !ok {
				continue
			}
			if req.ServiceName != nil || req.AppName != nil || req.SpanNamePrefix != nil {
				traces = filterTracesV2(traces, req)
				if traces == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamTracesResponse{Traces: traces, IsHistory: true}); err != nil {
				return err
			}
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case item, ok := <-ch:
			if !ok {
				return nil
			}
			if req.ServiceName != nil || req.AppName != nil || req.SpanNamePrefix != nil {
				item = filterTracesV2(item, req)
				if item == nil {
					continue
				}
			}
			if err := stream.Send(&agentpbv2.StreamTracesResponse{Traces: item}); err != nil {
				return err
			}
		}
	}
}

// filterLogsV2 filters log records based on the v2 stream request filters.
// Returns nil if all records are filtered out.
func filterLogsV2(req *otelpb.ExportLogsServiceRequest, filter *agentpbv2.StreamLogsRequest) *otelpb.ExportLogsServiceRequest {
	if filter == nil {
		return req
	}

	serviceName := filter.ServiceName
	appName := filter.AppName
	var minSeverity int32
	if filter.MinSeverity != nil {
		minSeverity = *filter.MinSeverity
	}

	if serviceName == nil && appName == nil && minSeverity == 0 {
		return req
	}

	var filteredResourceLogs []*otelpb.ResourceLogs
	for _, rl := range req.GetResourceLogs() {
		if !matchResourceAttributes(rl.GetResource(), serviceName, appName) {
			continue
		}

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
					filteredScopeLogs = append(filteredScopeLogs, &otelpb.ScopeLogs{
						Scope:      sl.GetScope(),
						LogRecords: filteredRecords,
						SchemaUrl:  sl.GetSchemaUrl(),
					})
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

// filterMetricsV2 filters metrics based on the v2 stream request filters.
// Returns nil if all metrics are filtered out.
func filterMetricsV2(req *otelpb.ExportMetricsServiceRequest, filter *agentpbv2.StreamMetricsRequest) *otelpb.ExportMetricsServiceRequest {
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

// filterTracesV2 filters traces based on the v2 stream request filters.
// Returns nil if all spans are filtered out.
func filterTracesV2(req *otelpb.ExportTraceServiceRequest, filter *agentpbv2.StreamTracesRequest) *otelpb.ExportTraceServiceRequest {
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
