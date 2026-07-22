package services

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// meshDialDurationBoundsMs are the explicit histogram bucket upper bounds (ms)
// for mesh.dial.duration_ms, chosen to resolve both fast LAN-direct dials
// (single digit ms) and slower cloud-relay round trips (hundreds of ms) with
// a coarse tail bucket for outliers.
var meshDialDurationBoundsMs = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500}

// meshConnKey identifies one mesh.connections timeseries.
type meshConnKey struct {
	peer   int32
	mode   string
	result string
}

// meshBytesKey identifies one mesh.bytes timeseries.
type meshBytesKey struct {
	peer int32
	dir  string
}

// meshDialKey identifies one mesh.dial.duration_ms timeseries.
type meshDialKey struct {
	peer int32
	mode string
}

// meshHistogram is the cumulative bucket/sum/count state for one
// mesh.dial.duration_ms timeseries. Like the Sum counters below, it is never
// reset between publishes: OTel cumulative temporality expects monotonically
// increasing counts measured from a fixed start time.
type meshHistogram struct {
	bucketCounts []uint64 // len(meshDialDurationBoundsMs)+1
	sum          float64
	count        uint64
}

// MeshMetrics accumulates mesh data-plane connection metrics in memory and
// flushes them as OTel metrics on a 15s tick, mirroring the
// CollectAgentMetrics/publishProcessMetrics pattern used for process and
// container metrics. RecordDial and RecordBytes are cheap (mutex + map
// update) so callers on the dial/relay hot path can call them inline.
type MeshMetrics struct {
	pub    TelemetryPublisher
	logger *zap.Logger

	startTime time.Time

	mu    sync.Mutex
	conns map[meshConnKey]uint64
	bytes map[meshBytesKey]int64
	dials map[meshDialKey]*meshHistogram
}

// NewMeshMetrics builds a MeshMetrics that publishes through pub.
func NewMeshMetrics(pub TelemetryPublisher, logger *zap.Logger) *MeshMetrics {
	return &MeshMetrics{
		pub:       pub,
		logger:    logger,
		startTime: time.Now(),
		conns:     make(map[meshConnKey]uint64),
		bytes:     make(map[meshBytesKey]int64),
		dials:     make(map[meshDialKey]*meshHistogram),
	}
}

// RecordDial records the outcome and wall-clock duration of one dial attempt
// (either the LAN-direct leg or the broker-relay leg). Safe for concurrent
// use; safe to call on a nil *MeshMetrics (a no-op), so callers that
// construct a dialer without metrics wired up don't need extra nil checks.
func (m *MeshMetrics) RecordDial(peer int32, mode, result string, durMs float64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conns[meshConnKey{peer: peer, mode: mode, result: result}]++

	hk := meshDialKey{peer: peer, mode: mode}
	h, ok := m.dials[hk]
	if !ok {
		h = &meshHistogram{bucketCounts: make([]uint64, len(meshDialDurationBoundsMs)+1)}
		m.dials[hk] = h
	}
	h.sum += durMs
	h.count++
	idx := len(meshDialDurationBoundsMs)
	for i, bound := range meshDialDurationBoundsMs {
		if durMs <= bound {
			idx = i
			break
		}
	}
	h.bucketCounts[idx]++
}

// RecordBytes accumulates n bytes transferred in direction dir ("tx"/"rx")
// for peer. Safe for concurrent use; safe to call on a nil *MeshMetrics.
func (m *MeshMetrics) RecordBytes(peer int32, dir string, n int64) {
	if m == nil || n <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bytes[meshBytesKey{peer: peer, dir: dir}] += n
}

// Collect flushes accumulated mesh metrics as one OTel export every 15s until
// ctx is cancelled. Run it as a goroutine from main.
func (m *MeshMetrics) Collect(ctx context.Context) {
	resource := newAgentResource()
	ticker := time.NewTicker(metricsCollectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			m.publish(resource, t)
		}
	}
}

// publish snapshots the accumulated counters and builds+publishes one
// ExportMetricsServiceRequest. Snapshotting under the lock (rather than
// holding it across PublishMetrics) keeps RecordDial/RecordBytes from ever
// blocking on network/disk I/O in the publisher.
func (m *MeshMetrics) publish(resource *otelpb.Resource, t time.Time) {
	m.mu.Lock()
	connsSnap := make(map[meshConnKey]uint64, len(m.conns))
	for k, v := range m.conns {
		connsSnap[k] = v
	}
	bytesSnap := make(map[meshBytesKey]int64, len(m.bytes))
	for k, v := range m.bytes {
		bytesSnap[k] = v
	}
	dialsSnap := make(map[meshDialKey]meshHistogram, len(m.dials))
	for k, h := range m.dials {
		counts := make([]uint64, len(h.bucketCounts))
		copy(counts, h.bucketCounts)
		dialsSnap[k] = meshHistogram{bucketCounts: counts, sum: h.sum, count: h.count}
	}
	m.mu.Unlock()

	if len(connsSnap) == 0 && len(bytesSnap) == 0 && len(dialsSnap) == 0 {
		return
	}

	nowNano := uint64(t.UnixNano())
	startNano := uint64(m.startTime.UnixNano())
	metrics := make([]*otelpb.Metric, 0, 3)

	if len(connsSnap) > 0 {
		dps := make([]*otelpb.NumberDataPoint, 0, len(connsSnap))
		for k, v := range connsSnap {
			dps = append(dps, &otelpb.NumberDataPoint{
				Attributes: []*otelpb.KeyValue{
					stringKV("mesh.peer", strconv.Itoa(int(k.peer))),
					stringKV("mesh.mode", k.mode),
					stringKV("mesh.result", k.result),
				},
				StartTimeUnixNano: startNano,
				TimeUnixNano:      nowNano,
				Value:             &otelpb.NumberDataPoint_AsInt{AsInt: int64(v)},
			})
		}
		metrics = append(metrics, &otelpb.Metric{
			Name: "mesh.connections",
			Unit: "1",
			Data: &otelpb.Metric_Sum{
				Sum: &otelpb.Sum{
					IsMonotonic:            true,
					AggregationTemporality: otelpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
					DataPoints:             dps,
				},
			},
		})
	}

	if len(bytesSnap) > 0 {
		dps := make([]*otelpb.NumberDataPoint, 0, len(bytesSnap))
		for k, v := range bytesSnap {
			dps = append(dps, &otelpb.NumberDataPoint{
				Attributes: []*otelpb.KeyValue{
					stringKV("mesh.peer", strconv.Itoa(int(k.peer))),
					stringKV("mesh.dir", k.dir),
				},
				StartTimeUnixNano: startNano,
				TimeUnixNano:      nowNano,
				Value:             &otelpb.NumberDataPoint_AsInt{AsInt: v},
			})
		}
		metrics = append(metrics, &otelpb.Metric{
			Name: "mesh.bytes",
			Unit: "By",
			Data: &otelpb.Metric_Sum{
				Sum: &otelpb.Sum{
					IsMonotonic:            true,
					AggregationTemporality: otelpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
					DataPoints:             dps,
				},
			},
		})
	}

	if len(dialsSnap) > 0 {
		dps := make([]*otelpb.HistogramDataPoint, 0, len(dialsSnap))
		for k, h := range dialsSnap {
			sum := h.sum
			dps = append(dps, &otelpb.HistogramDataPoint{
				Attributes: []*otelpb.KeyValue{
					stringKV("mesh.peer", strconv.Itoa(int(k.peer))),
					stringKV("mesh.mode", k.mode),
				},
				StartTimeUnixNano: startNano,
				TimeUnixNano:      nowNano,
				Count:             h.count,
				Sum:               &sum,
				BucketCounts:      h.bucketCounts,
				ExplicitBounds:    meshDialDurationBoundsMs,
			})
		}
		metrics = append(metrics, &otelpb.Metric{
			Name: "mesh.dial.duration_ms",
			Unit: "ms",
			Data: &otelpb.Metric_Histogram{
				Histogram: &otelpb.Histogram{
					AggregationTemporality: otelpb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
					DataPoints:             dps,
				},
			},
		})
	}

	m.pub.PublishMetrics(&otelpb.ExportMetricsServiceRequest{
		ResourceMetrics: []*otelpb.ResourceMetrics{
			{
				Resource: resource,
				ScopeMetrics: []*otelpb.ScopeMetrics{
					{
						Scope:   &otelpb.InstrumentationScope{Name: "wendy.mesh"},
						Metrics: metrics,
					},
				},
			},
		},
	})
}
