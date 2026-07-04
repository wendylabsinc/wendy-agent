package services

import (
	"strings"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

const (
	// PII / payload size guards applied to OTLP records before cloud export.
	maxLogBodyBytes = 65536 // 64 KiB — prevents oversized payloads
	maxLabelKeyLen  = 256
	maxLabelValLen  = 1024
	maxLabels       = 64
)

// sensitiveLabelDenyList contains substrings that, when found in an attribute
// key (case-insensitive), indicate the value may contain credentials or PII.
// Such attributes are dropped before export to prevent accidental exposure.
var sensitiveLabelDenyList = []string{
	"password", "passwd", "secret", "token", "authorization",
	"api_key", "apikey", "private_key", "credential", "auth",
	"cookie", "session",
}

// isSensitiveLabelKey reports whether key contains a deny-listed substring.
func isSensitiveLabelKey(key string) bool {
	lower := strings.ToLower(key)
	for _, denied := range sensitiveLabelDenyList {
		if strings.Contains(lower, denied) {
			return true
		}
	}
	return false
}

// sanitizeAttributes returns a new attribute slice with deny-listed keys
// removed, keys and string values truncated, and the total count capped at
// maxLabels. The input slice and its elements are not modified.
func sanitizeAttributes(attrs []*otelpb.KeyValue) []*otelpb.KeyValue {
	if len(attrs) == 0 {
		return attrs
	}
	out := make([]*otelpb.KeyValue, 0, min(len(attrs), maxLabels))
	for _, attr := range attrs {
		if len(out) >= maxLabels {
			break
		}
		k := attr.GetKey()
		if isSensitiveLabelKey(k) {
			continue
		}
		if len(k) > maxLabelKeyLen {
			k = k[:maxLabelKeyLen]
		}
		v := attr.GetValue()
		if s := v.GetStringValue(); len(s) > maxLabelValLen {
			v = &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: s[:maxLabelValLen]}}
		}
		out = append(out, &otelpb.KeyValue{Key: k, Value: v})
	}
	return out
}

// sanitizeLogs scrubs resource and log-record attributes and truncates oversized
// log bodies, mutating req in place.
func sanitizeLogs(req *otelpb.ExportLogsServiceRequest) {
	if req == nil {
		return
	}
	for _, rl := range req.GetResourceLogs() {
		if res := rl.GetResource(); res != nil {
			res.Attributes = sanitizeAttributes(res.GetAttributes())
		}
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				lr.Attributes = sanitizeAttributes(lr.GetAttributes())
				if body := lr.GetBody(); body != nil {
					if s := body.GetStringValue(); len(s) > maxLogBodyBytes {
						lr.Body = &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: s[:maxLogBodyBytes]}}
					}
				}
			}
		}
	}
}

// sanitizeMetrics scrubs resource attributes and every data-point's attributes
// across all metric data types, mutating req in place.
func sanitizeMetrics(req *otelpb.ExportMetricsServiceRequest) {
	if req == nil {
		return
	}
	for _, rm := range req.GetResourceMetrics() {
		if res := rm.GetResource(); res != nil {
			res.Attributes = sanitizeAttributes(res.GetAttributes())
		}
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				sanitizeMetricDataPoints(m)
			}
		}
	}
}

// sanitizeMetricDataPoints scrubs the attributes on each data point of m,
// dispatching on the metric's data type.
func sanitizeMetricDataPoints(m *otelpb.Metric) {
	switch data := m.GetData().(type) {
	case *otelpb.Metric_Gauge:
		for _, dp := range data.Gauge.GetDataPoints() {
			dp.Attributes = sanitizeAttributes(dp.GetAttributes())
		}
	case *otelpb.Metric_Sum:
		for _, dp := range data.Sum.GetDataPoints() {
			dp.Attributes = sanitizeAttributes(dp.GetAttributes())
		}
	case *otelpb.Metric_Histogram:
		for _, dp := range data.Histogram.GetDataPoints() {
			dp.Attributes = sanitizeAttributes(dp.GetAttributes())
		}
	case *otelpb.Metric_ExponentialHistogram:
		for _, dp := range data.ExponentialHistogram.GetDataPoints() {
			dp.Attributes = sanitizeAttributes(dp.GetAttributes())
		}
	case *otelpb.Metric_Summary:
		for _, dp := range data.Summary.GetDataPoints() {
			dp.Attributes = sanitizeAttributes(dp.GetAttributes())
		}
	}
}

// sanitizeTraces scrubs resource, span, span-event, and span-link attributes,
// mutating req in place.
func sanitizeTraces(req *otelpb.ExportTraceServiceRequest) {
	if req == nil {
		return
	}
	for _, rs := range req.GetResourceSpans() {
		if res := rs.GetResource(); res != nil {
			res.Attributes = sanitizeAttributes(res.GetAttributes())
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				sp.Attributes = sanitizeAttributes(sp.GetAttributes())
				for _, ev := range sp.GetEvents() {
					ev.Attributes = sanitizeAttributes(ev.GetAttributes())
				}
				for _, ln := range sp.GetLinks() {
					ln.Attributes = sanitizeAttributes(ln.GetAttributes())
				}
			}
		}
	}
}
