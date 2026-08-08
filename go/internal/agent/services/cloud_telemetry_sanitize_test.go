package services

import (
	"strings"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func kv(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   key,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: val}},
	}
}

func TestSanitizeAttributes_DropsSensitiveAndTruncates(t *testing.T) {
	in := []*commonpb.KeyValue{
		kv("user", "alice"),
		kv("password", "hunter2"),       // dropped by deny-list
		kv("auth_header", "Bearer xyz"), // dropped (contains "auth")
		kv(strings.Repeat("k", maxLabelKeyLen+10), "v"),
		kv("big", strings.Repeat("v", maxLabelValLen+10)),
	}
	out := sanitizeAttributes(in)

	for _, a := range out {
		if isSensitiveLabelKey(a.GetKey()) {
			t.Errorf("sensitive key survived: %q", a.GetKey())
		}
		if len(a.GetKey()) > maxLabelKeyLen {
			t.Errorf("key not truncated: len=%d", len(a.GetKey()))
		}
		if len(a.GetValue().GetStringValue()) > maxLabelValLen {
			t.Errorf("value not truncated: len=%d", len(a.GetValue().GetStringValue()))
		}
	}
	if len(out) != 3 { // user, truncated-key, big
		t.Fatalf("want 3 surviving attrs, got %d", len(out))
	}
}

func TestSanitizeAttributes_CapsCount(t *testing.T) {
	in := make([]*commonpb.KeyValue, 0, maxLabels+10)
	for i := 0; i < maxLabels+10; i++ {
		in = append(in, kv("k", "v"))
	}
	if got := len(sanitizeAttributes(in)); got != maxLabels {
		t.Fatalf("want %d (capped), got %d", maxLabels, got)
	}
}

func TestSanitizeLogs_TruncatesBodyAndScrubsAttrs(t *testing.T) {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{kv("token", "abc")}},
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{{
					Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: strings.Repeat("x", maxLogBodyBytes+100)}},
					Attributes: []*commonpb.KeyValue{kv("secret", "s"), kv("ok", "1")},
				}},
			}},
		}},
	}
	sanitizeLogs(req)

	rl := req.GetResourceLogs()[0]
	if len(rl.GetResource().GetAttributes()) != 0 {
		t.Errorf("resource token attr should be dropped")
	}
	lr := rl.GetScopeLogs()[0].GetLogRecords()[0]
	if len(lr.GetBody().GetStringValue()) != maxLogBodyBytes {
		t.Errorf("body not truncated to %d, got %d", maxLogBodyBytes, len(lr.GetBody().GetStringValue()))
	}
	if len(lr.GetAttributes()) != 1 || lr.GetAttributes()[0].GetKey() != "ok" {
		t.Errorf("record attrs not scrubbed correctly: %+v", lr.GetAttributes())
	}
}

func TestSanitizeMetrics_ScrubsDataPointAttrs(t *testing.T) {
	denyAttr := kv("api_key", "k")
	safeAttr := kv("region", "us")

	tests := []struct {
		name   string
		metric *metricspb.Metric
		getDP  func(m *metricspb.Metric) []*commonpb.KeyValue
	}{
		{
			name: "Gauge",
			metric: &metricspb.Metric{
				Name: "gauge",
				Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
					DataPoints: []*metricspb.NumberDataPoint{{
						Attributes: []*commonpb.KeyValue{denyAttr, safeAttr},
					}},
				}},
			},
			getDP: func(m *metricspb.Metric) []*commonpb.KeyValue {
				return m.GetGauge().GetDataPoints()[0].GetAttributes()
			},
		},
		{
			name: "Sum",
			metric: &metricspb.Metric{
				Name: "sum",
				Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
					DataPoints: []*metricspb.NumberDataPoint{{
						Attributes: []*commonpb.KeyValue{denyAttr, safeAttr},
					}},
				}},
			},
			getDP: func(m *metricspb.Metric) []*commonpb.KeyValue {
				return m.GetSum().GetDataPoints()[0].GetAttributes()
			},
		},
		{
			name: "Histogram",
			metric: &metricspb.Metric{
				Name: "histogram",
				Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
					DataPoints: []*metricspb.HistogramDataPoint{{
						Attributes: []*commonpb.KeyValue{denyAttr, safeAttr},
					}},
				}},
			},
			getDP: func(m *metricspb.Metric) []*commonpb.KeyValue {
				return m.GetHistogram().GetDataPoints()[0].GetAttributes()
			},
		},
		{
			name: "ExponentialHistogram",
			metric: &metricspb.Metric{
				Name: "exp_histogram",
				Data: &metricspb.Metric_ExponentialHistogram{ExponentialHistogram: &metricspb.ExponentialHistogram{
					DataPoints: []*metricspb.ExponentialHistogramDataPoint{{
						Attributes: []*commonpb.KeyValue{denyAttr, safeAttr},
					}},
				}},
			},
			getDP: func(m *metricspb.Metric) []*commonpb.KeyValue {
				return m.GetExponentialHistogram().GetDataPoints()[0].GetAttributes()
			},
		},
		{
			name: "Summary",
			metric: &metricspb.Metric{
				Name: "summary",
				Data: &metricspb.Metric_Summary{Summary: &metricspb.Summary{
					DataPoints: []*metricspb.SummaryDataPoint{{
						Attributes: []*commonpb.KeyValue{denyAttr, safeAttr},
					}},
				}},
			},
			getDP: func(m *metricspb.Metric) []*commonpb.KeyValue {
				return m.GetSummary().GetDataPoints()[0].GetAttributes()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &colmetricspb.ExportMetricsServiceRequest{
				ResourceMetrics: []*metricspb.ResourceMetrics{{
					ScopeMetrics: []*metricspb.ScopeMetrics{{
						Metrics: []*metricspb.Metric{tc.metric},
					}},
				}},
			}
			sanitizeMetrics(req)

			m := req.GetResourceMetrics()[0].GetScopeMetrics()[0].GetMetrics()[0]
			attrs := tc.getDP(m)
			if len(attrs) != 1 || attrs[0].GetKey() != "region" {
				t.Errorf("datapoint attrs not scrubbed correctly: %+v", attrs)
			}
		})
	}
}

func TestSanitizeTraces_ScrubsSpanAttrs(t *testing.T) {
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Name:       "s",
					Attributes: []*commonpb.KeyValue{kv("credential", "c"), kv("route", "/x")},
					Events: []*tracepb.Span_Event{{
						Name:       "e",
						Attributes: []*commonpb.KeyValue{kv("api_key", "secret"), kv("event_type", "click")},
					}},
					Links: []*tracepb.Span_Link{{
						Attributes: []*commonpb.KeyValue{kv("token", "t"), kv("link_kind", "parent")},
					}},
				}},
			}},
		}},
	}
	sanitizeTraces(req)

	sp := req.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0]
	if len(sp.GetAttributes()) != 1 || sp.GetAttributes()[0].GetKey() != "route" {
		t.Errorf("span attrs not scrubbed: %+v", sp.GetAttributes())
	}
	evAttrs := sp.GetEvents()[0].GetAttributes()
	if len(evAttrs) != 1 || evAttrs[0].GetKey() != "event_type" {
		t.Errorf("event attrs not scrubbed: %+v", evAttrs)
	}
	lnAttrs := sp.GetLinks()[0].GetAttributes()
	if len(lnAttrs) != 1 || lnAttrs[0].GetKey() != "link_kind" {
		t.Errorf("link attrs not scrubbed: %+v", lnAttrs)
	}
}
