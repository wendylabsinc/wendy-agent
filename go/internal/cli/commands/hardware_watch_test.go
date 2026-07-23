package commands

import (
	"encoding/json"
	"strings"
	"testing"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

func liveUSB(desc string, props map[string]string) *agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability {
	return &agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability{
		Category:    "usb",
		Description: desc,
		Properties:  props,
	}
}

func watched(vendor, product, serial string) *agentpbv2.WatchedUSBDevice {
	w := &agentpbv2.WatchedUSBDevice{VendorId: vendor, ProductId: product}
	if serial != "" {
		w.Serial = &serial
	}
	return w
}

func TestBuildWatchChecklist(t *testing.T) {
	live := []*agentpbv2.ListHardwareCapabilitiesResponse_HardwareCapability{
		liveUSB("canable2 gs_usb (1d50:606f)", map[string]string{
			"vendor_id": "1d50", "product_id": "606f", "serial": "A", "port_path": "1-2.2",
		}),
		liveUSB("canable2 gs_usb (1d50:606f)", map[string]string{
			"vendor_id": "1d50", "product_id": "606f", "serial": "B", "port_path": "1-2.4",
		}),
		// Root hub: hidden from the picker.
		liveUSB("xHCI Host Controller (1d6b:0002)", map[string]string{
			"vendor_id": "1d6b", "product_id": "0002",
		}),
	}
	// One unit watched, plus a watch for a device not on the bus anymore.
	watches := []*agentpbv2.WatchedUSBDevice{
		watched("1d50", "606f", "B"),
		watched("13d3", "3549", ""),
	}

	items := buildWatchChecklist(live, watches)
	if len(items) != 3 { // two CANables + absent bluetooth; root hub hidden
		t.Fatalf("expected 3 items, got %d: %+v", len(items), items)
	}

	if items[0].Selected {
		t.Error("serial A should not be pre-selected (watch pins serial B)")
	}
	if !items[1].Selected {
		t.Error("serial B should be pre-selected")
	}
	if !strings.Contains(items[1].Description, "serial B") || !strings.Contains(items[1].Description, "port 1-2.4") {
		t.Errorf("description missing identity: %q", items[1].Description)
	}

	// The item value round-trips to a valid watch entry with serial + label.
	var w agentpbv2.WatchedUSBDevice
	if err := json.Unmarshal([]byte(items[1].Value), &w); err != nil {
		t.Fatalf("value not JSON: %v", err)
	}
	if w.GetVendorId() != "1d50" || w.GetSerial() != "B" || w.GetLabel() != "canable2 gs_usb" {
		t.Errorf("round-tripped watch = %+v", &w)
	}

	// Absent watched device stays visible, checked, and flagged.
	absent := items[2]
	if !absent.Selected || !strings.Contains(absent.Description, "not currently connected") {
		t.Errorf("absent watch item = %+v", absent)
	}
}

func TestWatchCoversDevice(t *testing.T) {
	if !watchCoversDevice(watched("1D50", "606F", ""), "1d50", "606f", "anything") {
		t.Error("loose watch should match any serial, case-insensitively")
	}
	if !watchCoversDevice(watched("1d50", "606f", "B"), "1d50", "606f", "B") {
		t.Error("pinned watch should match its serial")
	}
	if watchCoversDevice(watched("1d50", "606f", "B"), "1d50", "606f", "A") {
		t.Error("pinned watch must not match another serial")
	}
	if watchCoversDevice(watched("1d50", "606f", ""), "16d0", "117e", "") {
		t.Error("different ids must not match")
	}
}

func TestApplyWatchEdits(t *testing.T) {
	current := []*agentpbv2.WatchedUSBDevice{watched("1d50", "606f", "A")}

	out, err := applyWatchEdits(current, []string{"16D0:117E:S1", "13d3:3549"}, nil)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(out) != 3 || out[1].GetVendorId() != "16d0" || out[1].GetSerial() != "S1" || out[2].GetSerial() != "" {
		t.Errorf("after add = %+v", out)
	}

	// Duplicate add is a no-op; remove drops the exact entry.
	out, err = applyWatchEdits(out, []string{"1d50:606f:A"}, []string{"13d3:3549"})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("after dedupe+remove = %+v", out)
	}

	if _, err := applyWatchEdits(nil, []string{"junk"}, nil); err == nil {
		t.Error("expected error for malformed spec")
	}
}
