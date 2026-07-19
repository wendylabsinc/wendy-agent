package mcp

import (
	"testing"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func TestFlattenHardwareEvent(t *testing.T) {
	ts := time.Date(2026, 7, 19, 22, 14, 0, 0, time.UTC)
	lr := &otelpb.LogRecord{
		TimeUnixNano: uint64(ts.UnixNano()),
		SeverityText: "WARN",
		Body:         &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "usb device disconnected: CANable2 (1d50:606f) at 1-2.4"}},
		Attributes: []*otelpb.KeyValue{
			{Key: "wendy.hardware.action", Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "disconnected"}}},
			{Key: "wendy.hardware.vendor_id", Value: &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: "1d50"}}},
		},
	}
	entry := flattenHardwareEvent(lr)
	if entry["timestamp"] != "2026-07-19T22:14:00Z" || entry["severity"] != "WARN" {
		t.Errorf("timestamp/severity = %v / %v", entry["timestamp"], entry["severity"])
	}
	if entry["action"] != "disconnected" || entry["vendor_id"] != "1d50" {
		t.Errorf("flattened attrs = %v", entry)
	}
	if _, hasPrefixed := entry["wendy.hardware.action"]; hasPrefixed {
		t.Error("prefix should be stripped")
	}
}

func TestSplitWatchSpecs(t *testing.T) {
	if got := splitWatchSpecs(""); got != nil {
		t.Errorf("empty = %v", got)
	}
	got := splitWatchSpecs(" 1d50:606f:S1 , 16d0:117e ,")
	if len(got) != 2 || got[0] != "1d50:606f:S1" || got[1] != "16d0:117e" {
		t.Errorf("split = %v", got)
	}
}

func TestApplyWatchSpecEdits(t *testing.T) {
	serial := "S1"
	current := []*agentpbv2.WatchedUSBDevice{
		{VendorId: "1d50", ProductId: "606f", Serial: &serial},
	}

	out, err := applyWatchSpecEdits(current, []string{"16D0:117E", "1d50:606f:S1"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(out) != 2 || out[1].GetVendorId() != "16d0" {
		t.Errorf("after add+dedupe = %v", out)
	}

	out, err = applyWatchSpecEdits(out, nil, []string{"1d50:606f:S1"})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(out) != 1 || out[0].GetVendorId() != "16d0" {
		t.Errorf("after remove = %v", out)
	}

	if _, err := applyWatchSpecEdits(nil, []string{"nocolon"}, nil); err == nil {
		t.Error("expected error for malformed spec")
	}
	if _, err := applyWatchSpecEdits(nil, []string{":606f"}, nil); err == nil {
		t.Error("expected error for empty vendor")
	}
}
