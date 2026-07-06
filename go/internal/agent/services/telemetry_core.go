package services

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

var resolveHostname = sync.OnceValue(func() string {
	h, _ := os.Hostname()
	return h
})

// newAgentResource builds the OTel resource for the wendy-agent process.
func newAgentResource() *otelpb.Resource {
	attrs := []*otelpb.KeyValue{
		stringKV("service.name", "wendy-agent"),
		stringKV("service.namespace", "wendy"),
		stringKV("service.version", version.Version),
	}
	if h := resolveHostname(); h != "" {
		attrs = append(attrs, stringKV("service.instance.id", h))
	}
	return &otelpb.Resource{Attributes: attrs}
}

// TelemetryCore is a zapcore.Core that publishes log entries to a
// TelemetryPublisher as OTEL log records. This bridges the agent's
// internal zap logger to the telemetry stream so that agent logs are
// visible via `wendy device logs --service wendy-agent`.
type TelemetryCore struct {
	broadcaster TelemetryPublisher
	level       zapcore.Level
	fields      []zapcore.Field
	resource    *otelpb.Resource
}

// NewTelemetryCore creates a new TelemetryCore that publishes to the given broadcaster.
func NewTelemetryCore(broadcaster TelemetryPublisher, level zapcore.Level) *TelemetryCore {
	return &TelemetryCore{
		broadcaster: broadcaster,
		level:       level,
		resource:    newAgentResource(),
	}
}

func stringKV(key, val string) *otelpb.KeyValue {
	return &otelpb.KeyValue{
		Key:   key,
		Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: val}},
	}
}

func (c *TelemetryCore) Enabled(level zapcore.Level) bool {
	return level >= c.level
}

func (c *TelemetryCore) With(fields []zapcore.Field) zapcore.Core {
	combined := make([]zapcore.Field, len(c.fields)+len(fields))
	copy(combined, c.fields)
	copy(combined[len(c.fields):], fields)
	return &TelemetryCore{
		broadcaster: c.broadcaster,
		level:       c.level,
		fields:      combined,
		resource:    c.resource,
	}
}

func (c *TelemetryCore) Check(entry zapcore.Entry, ce *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		ce = ce.AddCore(entry, c)
	}
	return ce
}

func (c *TelemetryCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	now := uint64(entry.Time.UnixNano())

	// Build attributes from accumulated fields + call-site fields.
	allFields := make([]zapcore.Field, 0, len(c.fields)+len(fields))
	allFields = append(allFields, c.fields...)
	allFields = append(allFields, fields...)

	attrs := make([]*otelpb.KeyValue, 0, len(allFields)+1)
	if entry.LoggerName != "" {
		attrs = append(attrs, &otelpb.KeyValue{
			Key:   "logger",
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: entry.LoggerName}},
		})
	}
	for _, f := range allFields {
		if kv := fieldToKeyValue(f); kv != nil {
			attrs = append(attrs, kv)
		}
	}

	severity, severityText := zapLevelToOTEL(entry.Level)

	record := &otelpb.LogRecord{
		TimeUnixNano:         now,
		ObservedTimeUnixNano: uint64(time.Now().UnixNano()),
		SeverityNumber:       severity,
		SeverityText:         severityText,
		Body: &otelpb.AnyValue{
			Value: &otelpb.AnyValue_StringValue{StringValue: entry.Message},
		},
		Attributes: attrs,
	}

	c.broadcaster.PublishLogs(&otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{
			{
				Resource: c.resource,
				ScopeLogs: []*otelpb.ScopeLogs{
					{
						Scope:      &otelpb.InstrumentationScope{Name: "wendy.agent"},
						LogRecords: []*otelpb.LogRecord{record},
					},
				},
			},
		},
	})

	return nil
}

func (c *TelemetryCore) Sync() error {
	return nil
}

// zapLevelToOTEL maps a zap log level to an OTEL severity number and text.
func zapLevelToOTEL(level zapcore.Level) (otelpb.SeverityNumber, string) {
	switch level {
	case zapcore.DebugLevel:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG, "DEBUG"
	case zapcore.InfoLevel:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
	case zapcore.WarnLevel:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"
	case zapcore.ErrorLevel:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR, "ERROR"
	case zapcore.DPanicLevel, zapcore.PanicLevel, zapcore.FatalLevel:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL, "FATAL"
	default:
		return otelpb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED, "UNSPECIFIED"
	}
}

// fieldToKeyValue converts a zap field to an OTEL KeyValue attribute.
func fieldToKeyValue(f zapcore.Field) *otelpb.KeyValue {
	switch f.Type {
	case zapcore.StringType:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: f.String}},
		}
	case zapcore.BoolType:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_BoolValue{BoolValue: f.Integer != 0}},
		}
	case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: f.Integer}},
		}
	case zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: f.Integer}},
		}
	case zapcore.Float64Type:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_DoubleValue{DoubleValue: math.Float64frombits(uint64(f.Integer))}},
		}
	case zapcore.Float32Type:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_DoubleValue{DoubleValue: float64(math.Float32frombits(uint32(f.Integer)))}},
		}
	case zapcore.DurationType:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: time.Duration(f.Integer).String()}},
		}
	case zapcore.ErrorType:
		if f.Interface != nil {
			return &otelpb.KeyValue{
				Key:   f.Key,
				Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: f.Interface.(error).Error()}},
			}
		}
		return nil
	case zapcore.TimeType:
		// Integer holds the time in UnixNano; Interface holds *time.Location.
		// zap.Time always sets a non-nil location, so the nil guard below is
		// only defensive against hand-built zapcore.Field values. Without this
		// case zap.Time falls through to default and exports the location
		// name (e.g. "UTC") instead of the timestamp.
		ts := time.Unix(0, f.Integer)
		if loc, ok := f.Interface.(*time.Location); ok && loc != nil {
			ts = ts.In(loc)
		}
		return stringKV(f.Key, ts.Format(time.RFC3339Nano))
	case zapcore.TimeFullType:
		if t, ok := f.Interface.(time.Time); ok {
			return stringKV(f.Key, t.Format(time.RFC3339Nano))
		}
		return nil
	case zapcore.ByteStringType:
		if b, ok := f.Interface.([]byte); ok {
			return stringKV(f.Key, string(b))
		}
		return nil
	case zapcore.Complex128Type, zapcore.Complex64Type:
		return stringKV(f.Key, fmt.Sprint(f.Interface))
	case zapcore.UintptrType:
		return &otelpb.KeyValue{
			Key:   f.Key,
			Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_IntValue{IntValue: f.Integer}},
		}
	case zapcore.ReflectType:
		// Covers zap.Any(structOrSlice). JSON-encode for a faithful,
		// queryable representation; fall back to fmt.Sprint on marshal error.
		if f.Interface == nil {
			return nil
		}
		if b, err := json.Marshal(f.Interface); err == nil {
			return stringKV(f.Key, string(b))
		}
		return stringKV(f.Key, fmt.Sprint(f.Interface))
	case zapcore.NamespaceType, zapcore.SkipType:
		// Namespaces carry no value; Skip fields are intentionally empty.
		return nil
	case zapcore.StringerType:
		if f.Interface != nil {
			return &otelpb.KeyValue{
				Key:   f.Key,
				Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: f.Interface.(fmt.Stringer).String()}},
			}
		}
		return nil
	default:
		// For complex types, use fmt.Sprint as a fallback.
		if f.Interface != nil {
			return &otelpb.KeyValue{
				Key:   f.Key,
				Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: fmt.Sprint(f.Interface)}},
			}
		}
		return nil
	}
}

// Compile-time check that TelemetryCore implements zapcore.Core.
var _ zapcore.Core = (*TelemetryCore)(nil)
