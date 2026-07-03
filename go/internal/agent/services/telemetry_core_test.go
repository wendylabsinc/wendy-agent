package services

import (
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// fieldToKeyValue is exercised via a real zap.Field so we cover how zap encodes
// each constructor, not just the zapcore.FieldType enum.
//
// Named fieldKV (not kv) to avoid colliding with the kv(key, val string)
// fixture helper already declared in cloud_telemetry_sanitize_test.go.
func fieldKV(t *testing.T, f zap.Field) *otelpb.KeyValue {
	t.Helper()
	return fieldToKeyValue(zapcore.Field(f))
}

func TestFieldToKeyValue_TimeExportsTimestampNotLocation(t *testing.T) {
	ts := time.Date(2026, 7, 3, 12, 30, 45, 123456789, time.UTC)
	got := fieldKV(t, zap.Time("ts", ts))
	if got == nil {
		t.Fatal("zap.Time produced a nil attribute")
	}
	sv, ok := got.Value.Value.(*otelpb.AnyValue_StringValue)
	if !ok {
		t.Fatalf("zap.Time value type = %T, want string", got.Value.Value)
	}
	if sv.StringValue != ts.Format(time.RFC3339Nano) {
		t.Errorf("zap.Time = %q, want RFC3339Nano %q (must not be a timezone name)",
			sv.StringValue, ts.Format(time.RFC3339Nano))
	}
}

func TestFieldToKeyValue_Primitives(t *testing.T) {
	if v := fieldKV(t, zap.String("k", "v")).Value.GetStringValue(); v != "v" {
		t.Errorf("string = %q", v)
	}
	if v := fieldKV(t, zap.Int("k", 7)).Value.GetIntValue(); v != 7 {
		t.Errorf("int = %d", v)
	}
	if v := fieldKV(t, zap.Bool("k", true)).Value.GetBoolValue(); v != true {
		t.Errorf("bool = %v", v)
	}
	if v := fieldKV(t, zap.Duration("k", 2*time.Second)).Value.GetStringValue(); v != "2s" {
		t.Errorf("duration = %q", v)
	}
}

func TestFieldToKeyValue_AnyStructIsJSON(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	got := fieldKV(t, zap.Any("k", payload{Name: "x", Count: 3}))
	if got == nil {
		t.Fatal("zap.Any(struct) produced nil")
	}
	if v := got.Value.GetStringValue(); v != `{"name":"x","count":3}` {
		t.Errorf("zap.Any(struct) = %q, want JSON", v)
	}
}

func TestFieldToKeyValue_NamespaceAndSkipAreDropped(t *testing.T) {
	if got := fieldKV(t, zap.Namespace("ns")); got != nil {
		t.Errorf("zap.Namespace = %+v, want nil", got)
	}
	if got := fieldKV(t, zap.Skip()); got != nil {
		t.Errorf("zap.Skip = %+v, want nil", got)
	}
}

func TestFieldToKeyValue_TimeFullType(t *testing.T) {
	// zap.Time produces TimeFullType (instead of TimeType) for times outside
	// the int64-nanos range.
	ts := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	got := fieldKV(t, zap.Time("ts", ts))
	if got == nil {
		t.Fatal("zap.Time (far future) produced a nil attribute")
	}
	sv, ok := got.Value.Value.(*otelpb.AnyValue_StringValue)
	if !ok {
		t.Fatalf("zap.Time (far future) value type = %T, want string", got.Value.Value)
	}
	if want := ts.Format(time.RFC3339Nano); sv.StringValue != want {
		t.Errorf("zap.Time (far future) = %q, want %q", sv.StringValue, want)
	}
}

// TestFieldToKeyValue_TimeFullTypeDirect constructs a TimeFullType field
// directly rather than relying on zap.Time's far-future routing, so the
// TimeFullType branch stays covered even if zap changes how zap.Time picks
// between TimeType and TimeFullType.
func TestFieldToKeyValue_TimeFullTypeDirect(t *testing.T) {
	ts := time.Date(2026, 7, 3, 12, 30, 45, 123456789, time.UTC)
	got := fieldToKeyValue(zapcore.Field{Key: "ts", Type: zapcore.TimeFullType, Interface: ts})
	if got == nil {
		t.Fatal("TimeFullType produced a nil attribute")
	}
	if want := ts.Format(time.RFC3339Nano); got.Value.GetStringValue() != want {
		t.Errorf("TimeFullType = %q, want %q", got.Value.GetStringValue(), want)
	}
}

func TestFieldToKeyValue_TimeTypeNonUTC(t *testing.T) {
	// Regression: TimeType must render the timestamp in its own offset, not
	// silently normalize to UTC.
	loc := time.FixedZone("test", 5*3600)
	ts := time.Date(2026, 7, 3, 12, 0, 0, 0, loc)
	got := fieldKV(t, zap.Time("ts", ts))
	if got == nil {
		t.Fatal("zap.Time (non-UTC) produced a nil attribute")
	}
	sv, ok := got.Value.Value.(*otelpb.AnyValue_StringValue)
	if !ok {
		t.Fatalf("zap.Time (non-UTC) value type = %T, want string", got.Value.Value)
	}
	want := ts.Format(time.RFC3339Nano)
	if sv.StringValue != want {
		t.Errorf("zap.Time (non-UTC) = %q, want %q", sv.StringValue, want)
	}
}

func TestFieldToKeyValue_ByteString(t *testing.T) {
	got := fieldKV(t, zap.ByteString("k", []byte("hi")))
	if got == nil {
		t.Fatal("zap.ByteString produced a nil attribute")
	}
	if v := got.Value.GetStringValue(); v != "hi" {
		t.Errorf("zap.ByteString = %q, want %q", v, "hi")
	}
}

func TestFieldToKeyValue_Complex128(t *testing.T) {
	got := fieldKV(t, zap.Complex128("k", complex(1, 2)))
	if got == nil {
		t.Fatal("zap.Complex128 produced a nil attribute")
	}
	want := fmt.Sprint(complex128(complex(1, 2)))
	if v := got.Value.GetStringValue(); v != want {
		t.Errorf("zap.Complex128 = %q, want %q", v, want)
	}
}

func TestFieldToKeyValue_Uintptr(t *testing.T) {
	got := fieldKV(t, zap.Uintptr("k", 0x42))
	if got == nil {
		t.Fatal("zap.Uintptr produced a nil attribute")
	}
	if v := got.Value.GetIntValue(); v != 66 {
		t.Errorf("zap.Uintptr = %d, want 66", v)
	}
}

func TestFieldToKeyValue_ReflectTypeMarshalErrorFallsBackToSprint(t *testing.T) {
	// A channel cannot be JSON-marshalled, so zap.Any routes to ReflectType
	// and json.Marshal errors, exercising the fmt.Sprint fallback.
	got := fieldKV(t, zap.Any("k", make(chan int)))
	if got == nil {
		t.Fatal("zap.Any(chan) produced a nil attribute, want fmt.Sprint fallback")
	}
	sv, ok := got.Value.Value.(*otelpb.AnyValue_StringValue)
	if !ok {
		t.Fatalf("zap.Any(chan) value type = %T, want string", got.Value.Value)
	}
	if sv.StringValue == "" {
		t.Error("zap.Any(chan) fallback string is empty")
	}
}
