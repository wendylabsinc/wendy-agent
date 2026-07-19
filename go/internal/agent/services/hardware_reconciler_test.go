package services

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func matcher(v, p string) appconfig.USBDeviceMatcher {
	return appconfig.USBDeviceMatcher{VendorID: v, ProductID: p}
}

func TestReconcileRequiredDevices_InitialMissingAlerts(t *testing.T) {
	state := make(map[requiredDeviceKey]bool)
	required := map[string][]appconfig.USBDeviceMatcher{
		"arms": {matcher("16d0", "117e"), matcher("1d50", "606f")},
	}
	present := map[string]bool{"16d0:117e": true} // second device absent

	changes := reconcileRequiredDevices(state, required, present)
	want := []requiredDeviceChange{{AppID: "arms", Device: "1d50:606f", Missing: true}}
	if !reflect.DeepEqual(changes, want) {
		t.Fatalf("changes = %v, want %v", changes, want)
	}

	// Second round with no change: silent.
	if changes := reconcileRequiredDevices(state, required, present); len(changes) != 0 {
		t.Fatalf("steady state produced changes: %v", changes)
	}
}

func TestReconcileRequiredDevices_Transitions(t *testing.T) {
	state := make(map[requiredDeviceKey]bool)
	required := map[string][]appconfig.USBDeviceMatcher{
		"arms": {matcher("16d0", "117e")},
	}

	// Initially present: recorded silently.
	if changes := reconcileRequiredDevices(state, required, map[string]bool{"16d0:117e": true}); len(changes) != 0 {
		t.Fatalf("initial present produced changes: %v", changes)
	}

	// Device drops: missing alert.
	changes := reconcileRequiredDevices(state, required, map[string]bool{})
	if len(changes) != 1 || !changes[0].Missing {
		t.Fatalf("expected missing alert, got %v", changes)
	}

	// Device returns: restored alert.
	changes = reconcileRequiredDevices(state, required, map[string]bool{"16d0:117e": true})
	if len(changes) != 1 || changes[0].Missing {
		t.Fatalf("expected restored alert, got %v", changes)
	}
}

func TestReconcileRequiredDevices_AppStopForgetsState(t *testing.T) {
	state := make(map[requiredDeviceKey]bool)
	required := map[string][]appconfig.USBDeviceMatcher{
		"arms": {matcher("16d0", "117e")},
	}
	reconcileRequiredDevices(state, required, map[string]bool{}) // missing alert

	// App stops: requirement disappears, no restored event, state cleaned.
	if changes := reconcileRequiredDevices(state, map[string][]appconfig.USBDeviceMatcher{}, map[string]bool{}); len(changes) != 0 {
		t.Fatalf("app stop produced changes: %v", changes)
	}
	if len(state) != 0 {
		t.Fatalf("state not cleaned: %v", state)
	}

	// App restarts while device still missing: alerts again (fresh key).
	changes := reconcileRequiredDevices(state, required, map[string]bool{})
	if len(changes) != 1 || !changes[0].Missing {
		t.Fatalf("expected fresh missing alert after restart, got %v", changes)
	}
}

func TestReconcileRequiredDevices_SharedDeviceAlertsPerApp(t *testing.T) {
	state := make(map[requiredDeviceKey]bool)
	required := map[string][]appconfig.USBDeviceMatcher{
		"arms":   {matcher("16d0", "117e")},
		"webcam": {matcher("16d0", "117e")},
	}
	changes := reconcileRequiredDevices(state, required, map[string]bool{})
	if len(changes) != 2 {
		t.Fatalf("expected one alert per app, got %v", changes)
	}
	// Deterministic order: sorted by app id.
	if changes[0].AppID != "arms" || changes[1].AppID != "webcam" {
		t.Errorf("unexpected order: %v", changes)
	}
}

func TestRequiredDeviceLogRecord(t *testing.T) {
	now := time.Unix(1700000000, 0)
	req := requiredDeviceLogRecord(hardwareEventsResource(),
		requiredDeviceChange{AppID: "arms", Device: "1d50:606f", Missing: true}, now)
	rec := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR {
		t.Errorf("missing severity = %v, want ERROR", rec.SeverityNumber)
	}
	body := rec.GetBody().GetStringValue()
	if !strings.Contains(body, "missing") || !strings.Contains(body, "1d50:606f") || !strings.Contains(body, "app arms") {
		t.Errorf("body = %q", body)
	}
	attrs := map[string]string{}
	for _, kv := range rec.GetAttributes() {
		attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	if attrs["wendy.hardware.action"] != "required_missing" || attrs["wendy.hardware.app"] != "arms" ||
		attrs["wendy.hardware.vendor_id"] != "1d50" || attrs["wendy.hardware.product_id"] != "606f" {
		t.Errorf("attrs = %v", attrs)
	}

	rec = requiredDeviceLogRecord(hardwareEventsResource(),
		requiredDeviceChange{AppID: "arms", Device: "1d50:606f", Missing: false}, now).
		ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("restored severity = %v, want INFO", rec.SeverityNumber)
	}
}
