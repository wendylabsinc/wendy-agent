package services

import (
	"strings"
	"testing"
	"time"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

func uevent(header string, pairs ...string) []byte {
	parts := append([]string{header}, pairs...)
	return []byte(strings.Join(parts, "\x00"))
}

func TestParseUEvent(t *testing.T) {
	kv, ok := parseUEvent(uevent(
		"add@/devices/platform/usb1/1-2/1-2.4",
		"ACTION=add",
		"DEVPATH=/devices/platform/usb1/1-2/1-2.4",
		"SUBSYSTEM=usb",
		"DEVTYPE=usb_device",
		"PRODUCT=16d0/117e/100",
		"SEQNUM=4711",
	))
	if !ok {
		t.Fatal("expected valid uevent")
	}
	if kv["ACTION"] != "add" || kv["SUBSYSTEM"] != "usb" || kv["PRODUCT"] != "16d0/117e/100" {
		t.Errorf("unexpected kv: %v", kv)
	}
}

func TestParseUEvent_RejectsNonKernelMessages(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("libudev\x00binary-blob"),
		[]byte("no-at-sign"),
		uevent("add@/devices/x", "ACTION=add"), // missing DEVPATH key
	}
	for _, c := range cases {
		if _, ok := parseUEvent(c); ok {
			t.Errorf("expected rejection for %q", c)
		}
	}
}

func TestUSBEventFromUEvent(t *testing.T) {
	kv := map[string]string{
		"ACTION":    "remove",
		"DEVPATH":   "/devices/platform/usb1/1-2/1-2.4",
		"SUBSYSTEM": "usb",
		"DEVTYPE":   "usb_device",
		"PRODUCT":   "16d0/117e/100",
	}
	ev, ok := usbEventFromUEvent(kv)
	if !ok {
		t.Fatal("expected usb event")
	}
	if ev.Action != usbEventDisconnected {
		t.Errorf("action = %q", ev.Action)
	}
	if ev.PortPath != "1-2.4" {
		t.Errorf("port path = %q", ev.PortPath)
	}
	if ev.VendorID != "16d0" || ev.ProductID != "117e" {
		t.Errorf("ids = %s:%s", ev.VendorID, ev.ProductID)
	}
}

func TestUSBEventFromUEvent_IgnoresInterfacesAndOtherActions(t *testing.T) {
	base := map[string]string{
		"ACTION":    "add",
		"DEVPATH":   "/devices/platform/usb1/1-2",
		"SUBSYSTEM": "usb",
		"DEVTYPE":   "usb_device",
	}
	iface := map[string]string{}
	for k, v := range base {
		iface[k] = v
	}
	iface["DEVTYPE"] = "usb_interface"
	bind := map[string]string{}
	for k, v := range base {
		bind[k] = v
	}
	bind["ACTION"] = "bind"
	block := map[string]string{}
	for k, v := range base {
		block[k] = v
	}
	block["SUBSYSTEM"] = "block"

	for name, kv := range map[string]map[string]string{"interface": iface, "bind": bind, "block": block} {
		if _, ok := usbEventFromUEvent(kv); ok {
			t.Errorf("%s: expected rejection", name)
		}
	}
	if _, ok := usbEventFromUEvent(base); !ok {
		t.Error("base usb_device add should be accepted")
	}
}

func TestParseUEventProduct_ZeroPads(t *testing.T) {
	v, p := parseUEventProduct("1d6b/2/515")
	if v != "1d6b" || p != "0002" {
		t.Errorf("got %s:%s, want 1d6b:0002", v, p)
	}
	if v, p := parseUEventProduct("garbage"); v != "" || p != "" {
		t.Errorf("expected empty ids for garbage, got %s:%s", v, p)
	}
	if v, p := parseUEventProduct("xyz/123/1"); v != "" || p != "0123" {
		t.Errorf("non-hex vendor should be empty, got %s:%s", v, p)
	}
}

func TestUSBEventLogRecord(t *testing.T) {
	now := time.Unix(1700000000, 0)
	ev := usbEvent{
		Action:    usbEventDisconnected,
		PortPath:  "1-2.4",
		VendorID:  "16d0",
		ProductID: "117e",
		Product:   "CANable2",
	}
	req := usbEventLogRecord(hardwareEventsResource(), ev, now)

	if got := resourceServiceName(req.ResourceLogs[0].GetResource()); got != hardwareEventsServiceName {
		t.Errorf("service.name = %q", got)
	}
	rec := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_WARN {
		t.Errorf("disconnect severity = %v, want WARN", rec.SeverityNumber)
	}
	body := rec.GetBody().GetStringValue()
	if body != "usb device disconnected: CANable2 (16d0:117e) at 1-2.4" {
		t.Errorf("body = %q", body)
	}
	attrs := map[string]string{}
	for _, kvp := range rec.GetAttributes() {
		attrs[kvp.GetKey()] = kvp.GetValue().GetStringValue()
	}
	for k, want := range map[string]string{
		"wendy.hardware.action":     "disconnected",
		"wendy.hardware.vendor_id":  "16d0",
		"wendy.hardware.product_id": "117e",
		"wendy.hardware.product":    "CANable2",
		"wendy.hardware.port_path":  "1-2.4",
	} {
		if attrs[k] != want {
			t.Errorf("attr %s = %q, want %q", k, attrs[k], want)
		}
	}

	ev.Action = usbEventConnected
	rec = usbEventLogRecord(hardwareEventsResource(), ev, now).ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_INFO {
		t.Errorf("connect severity = %v, want INFO", rec.SeverityNumber)
	}
}

func TestUSBEventDisplayName_Fallbacks(t *testing.T) {
	cases := []struct {
		ev   usbEvent
		want string
	}{
		{usbEvent{Product: "CANable2", VendorID: "16d0", ProductID: "117e"}, "CANable2 (16d0:117e)"},
		{usbEvent{VendorID: "16d0", ProductID: "117e"}, "16d0:117e"},
		{usbEvent{Product: "CANable2"}, "CANable2"},
		{usbEvent{}, "unknown device"},
	}
	for _, c := range cases {
		if got := usbEventDisplayName(c.ev); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.ev, got, c.want)
		}
	}
}

func TestUSBStormLogRecord(t *testing.T) {
	req := usbStormLogRecord(hardwareEventsResource(), 120, 50, time.Unix(1700000000, 0))
	rec := req.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if rec.SeverityNumber != otelpb.SeverityNumber_SEVERITY_NUMBER_WARN {
		t.Errorf("storm severity = %v, want WARN", rec.SeverityNumber)
	}
	if !strings.Contains(rec.GetBody().GetStringValue(), "120 events suppressed") {
		t.Errorf("body = %q", rec.GetBody().GetStringValue())
	}
}
