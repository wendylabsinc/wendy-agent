package services

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func TestHardwareWatchStore_RoundTrip(t *testing.T) {
	notify := make(chan struct{}, 1)
	store := NewHardwareWatchStore(filepath.Join(t.TempDir(), "sub", "watch.json"), notify)

	// Missing file: empty list, no error.
	devices, err := store.Load()
	if err != nil || devices != nil {
		t.Fatalf("Load on missing file = %v, %v", devices, err)
	}

	want := []WatchedDevice{
		{VendorID: "1d50", ProductID: "606f", Serial: "002B", Label: "canable2 gs_usb"},
		{VendorID: "13d3", ProductID: "3549"},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	select {
	case <-notify:
	default:
		t.Error("Save did not signal notify channel")
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestValidateWatchedDevice(t *testing.T) {
	if err := ValidateWatchedDevice(WatchedDevice{VendorID: "1D50", ProductID: "606f"}); err != nil {
		t.Errorf("mixed-case hex rejected: %v", err)
	}
	for _, w := range []WatchedDevice{
		{VendorID: "1d5", ProductID: "606f"},
		{VendorID: "1d50", ProductID: "606f0"},
		{VendorID: "xyzw", ProductID: "606f"},
		{VendorID: "", ProductID: "606f"},
	} {
		if err := ValidateWatchedDevice(w); err == nil {
			t.Errorf("expected rejection for %+v", w)
		}
	}
}

func TestReconcileWatchedDevices_GracePeriod(t *testing.T) {
	state := make(map[string]*watchState)
	watches := []WatchedDevice{{VendorID: "1d50", ProductID: "606f"}}
	grace := 30 * time.Second
	t0 := time.Unix(1700000000, 0)

	// Absent from the start: no alert before the grace period elapses.
	if ch := reconcileWatchedDevices(state, watches, nil, t0, grace); len(ch) != 0 {
		t.Fatalf("alert before grace: %v", ch)
	}
	if ch := reconcileWatchedDevices(state, watches, nil, t0.Add(10*time.Second), grace); len(ch) != 0 {
		t.Fatalf("alert at 10s: %v", ch)
	}
	// At/after grace: exactly one missing alert, not repeated.
	ch := reconcileWatchedDevices(state, watches, nil, t0.Add(30*time.Second), grace)
	if len(ch) != 1 || !ch[0].Missing {
		t.Fatalf("expected missing alert at grace, got %v", ch)
	}
	if ch := reconcileWatchedDevices(state, watches, nil, t0.Add(60*time.Second), grace); len(ch) != 0 {
		t.Fatalf("repeated missing alert: %v", ch)
	}

	// Device returns: restored fires once.
	present := []presentUSBDevice{{VendorID: "1d50", ProductID: "606f", Serial: "002B"}}
	ch = reconcileWatchedDevices(state, watches, present, t0.Add(90*time.Second), grace)
	if len(ch) != 1 || ch[0].Missing {
		t.Fatalf("expected restored, got %v", ch)
	}
	if ch := reconcileWatchedDevices(state, watches, present, t0.Add(2*time.Minute), grace); len(ch) != 0 {
		t.Fatalf("repeated restored: %v", ch)
	}
}

func TestReconcileWatchedDevices_QuickReplugStaysSilent(t *testing.T) {
	state := make(map[string]*watchState)
	watches := []WatchedDevice{{VendorID: "1d50", ProductID: "606f"}}
	grace := 30 * time.Second
	t0 := time.Unix(1700000000, 0)
	present := []presentUSBDevice{{VendorID: "1d50", ProductID: "606f"}}

	reconcileWatchedDevices(state, watches, present, t0, grace)
	// Gone at t+5, back at t+15: never crossed grace, no events at all.
	if ch := reconcileWatchedDevices(state, watches, nil, t0.Add(5*time.Second), grace); len(ch) != 0 {
		t.Fatalf("unexpected: %v", ch)
	}
	if ch := reconcileWatchedDevices(state, watches, present, t0.Add(15*time.Second), grace); len(ch) != 0 {
		t.Fatalf("restored without missing alert: %v", ch)
	}
}

func TestReconcileWatchedDevices_SerialPinning(t *testing.T) {
	state := make(map[string]*watchState)
	// Two identical adapters; watch pins serial B.
	watches := []WatchedDevice{{VendorID: "1d50", ProductID: "606f", Serial: "B"}}
	grace := time.Second
	t0 := time.Unix(1700000000, 0)

	onlyA := []presentUSBDevice{{VendorID: "1d50", ProductID: "606f", Serial: "A"}}
	// Serial B absent even though an identical vid:pid is present.
	reconcileWatchedDevices(state, watches, onlyA, t0, grace)
	ch := reconcileWatchedDevices(state, watches, onlyA, t0.Add(2*time.Second), grace)
	if len(ch) != 1 || !ch[0].Missing {
		t.Fatalf("expected missing for pinned serial, got %v", ch)
	}

	// Serial-less watch is satisfied by either unit.
	loose := []WatchedDevice{{VendorID: "1d50", ProductID: "606f"}}
	state2 := make(map[string]*watchState)
	reconcileWatchedDevices(state2, loose, onlyA, t0, grace)
	if ch := reconcileWatchedDevices(state2, loose, onlyA, t0.Add(2*time.Second), grace); len(ch) != 0 {
		t.Fatalf("loose watch should be satisfied: %v", ch)
	}
}

func TestReconcileWatchedDevices_RemovedWatchForgotten(t *testing.T) {
	state := make(map[string]*watchState)
	watches := []WatchedDevice{{VendorID: "1d50", ProductID: "606f"}}
	grace := time.Second
	t0 := time.Unix(1700000000, 0)

	reconcileWatchedDevices(state, watches, nil, t0, grace)
	reconcileWatchedDevices(state, watches, nil, t0.Add(2*time.Second), grace) // alerted

	// Watch removed: state cleaned, no restored event.
	if ch := reconcileWatchedDevices(state, nil, nil, t0.Add(3*time.Second), grace); len(ch) != 0 {
		t.Fatalf("removal produced events: %v", ch)
	}
	if len(state) != 0 {
		t.Fatalf("state not cleaned: %v", state)
	}
}

func TestWatchedDeviceLogRecord(t *testing.T) {
	now := time.Unix(1700000000, 0)
	w := WatchedDevice{VendorID: "1d50", ProductID: "606f", Serial: "002B", Label: "canable2 gs_usb"}

	rec := watchedDeviceLogRecord(hardwareEventsResource(), watchedChange{Watch: w, Missing: true}, now).
		ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR {
		t.Errorf("missing severity = %v, want ERROR", rec.SeverityNumber)
	}
	body := rec.GetBody().GetStringValue()
	if !strings.Contains(body, "missing") || !strings.Contains(body, "canable2 gs_usb") {
		t.Errorf("body = %q", body)
	}
	attrs := map[string]string{}
	for _, kv := range rec.GetAttributes() {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if attrs["wendy.hardware.action"] != "watched_missing" || attrs["wendy.hardware.serial"] != "002B" {
		t.Errorf("attrs = %v", attrs)
	}

	rec = watchedDeviceLogRecord(hardwareEventsResource(), watchedChange{Watch: w, Missing: false}, now).
		ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("restored severity = %v, want INFO", rec.SeverityNumber)
	}
}
