package services

import (
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
