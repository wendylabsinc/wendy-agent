package services

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// Pure kernel-uevent parsing and OTel record construction for USB hotplug
// events, shared between the Linux netlink collector (hardware_events_linux.go)
// and tests on non-Linux platforms — same split as kmsg_parse.go / dmesg.
//
// Events are published under their own service.name so the CLI can replay a
// device's peripheral timeline with a plain StreamLogs service filter.
const (
	hardwareEventsServiceName = "wendy.hardware"

	// usbEventsMaxPerSec caps forwarded events per second. A device stuck in a
	// re-enumeration loop (brownout, flaky cable) can emit hundreds of uevents
	// per second; the suppressed count is itself published as a storm event,
	// which is the more useful remote signal anyway.
	usbEventsMaxPerSec = 50

	// usbDeviceNameCacheMax bounds the devpath→display-name cache used to label
	// disconnect events. Missed removes (e.g. events dropped during a storm)
	// must not grow the cache without bound.
	usbDeviceNameCacheMax = 1024
)

const (
	usbEventConnected    = "connected"
	usbEventDisconnected = "disconnected"
)

// usbEvent is one hotplug transition of a USB device (not interface).
type usbEvent struct {
	Action    string // usbEventConnected / usbEventDisconnected
	DevPath   string // kernel DEVPATH (no /sys prefix)
	PortPath  string // sysfs name encoding physical topology, e.g. "1-2.4"
	VendorID  string // 4-digit lowercase hex
	ProductID string // 4-digit lowercase hex
	Product   string // human-readable product string, may be empty
}

// parseUEvent splits a kernel uevent datagram ("action@devpath\0KEY=VALUE\0…")
// into its key/value pairs. Returns false for datagrams that don't look like
// kernel uevents (e.g. udevd's "libudev" re-broadcasts).
func parseUEvent(data []byte) (map[string]string, bool) {
	fields := bytes.Split(data, []byte{0})
	if len(fields) == 0 {
		return nil, false
	}
	header := string(fields[0])
	at := strings.IndexByte(header, '@')
	if at <= 0 || !strings.HasPrefix(header[at+1:], "/") {
		return nil, false
	}
	kv := make(map[string]string, len(fields))
	for _, f := range fields[1:] {
		if len(f) == 0 {
			continue
		}
		s := string(f)
		eq := strings.IndexByte(s, '=')
		if eq <= 0 {
			continue
		}
		kv[s[:eq]] = s[eq+1:]
	}
	// Trust the explicit ACTION/DEVPATH keys over the header; the kernel always
	// sends both and they cannot disagree in practice.
	if kv["ACTION"] == "" || kv["DEVPATH"] == "" {
		return nil, false
	}
	return kv, true
}

// usbEventFromUEvent extracts a usbEvent from parsed uevent keys, or false if
// the uevent is not an add/remove of a whole USB device. Interface events
// (DEVTYPE=usb_interface) and bind/unbind driver events are ignored: one
// physical plug/unplug fires all of them, and the usb_device add/remove pair
// is the physically meaningful one.
func usbEventFromUEvent(kv map[string]string) (usbEvent, bool) {
	if kv["SUBSYSTEM"] != "usb" || kv["DEVTYPE"] != "usb_device" {
		return usbEvent{}, false
	}
	var action string
	switch kv["ACTION"] {
	case "add":
		action = usbEventConnected
	case "remove":
		action = usbEventDisconnected
	default:
		return usbEvent{}, false
	}
	devPath := kv["DEVPATH"]
	vendorID, productID := parseUEventProduct(kv["PRODUCT"])
	return usbEvent{
		Action:    action,
		DevPath:   devPath,
		PortPath:  path.Base(devPath),
		VendorID:  vendorID,
		ProductID: productID,
	}, true
}

// parseUEventProduct parses the uevent PRODUCT value ("vid/pid/bcdDevice",
// lowercase hex without leading zeros, e.g. "16d0/117e/100") into zero-padded
// 4-digit vendor and product ids matching sysfs idVendor/idProduct format.
func parseUEventProduct(product string) (vendorID, productID string) {
	parts := strings.Split(product, "/")
	if len(parts) < 2 {
		return "", ""
	}
	pad := func(s string) string {
		v, err := strconv.ParseUint(s, 16, 16)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%04x", v)
	}
	return pad(parts[0]), pad(parts[1])
}

// usbEventDisplayName renders "Product (vvvv:pppp)" with whichever parts are
// known, falling back to just ids or just the name.
func usbEventDisplayName(ev usbEvent) string {
	ids := ""
	if ev.VendorID != "" && ev.ProductID != "" {
		ids = ev.VendorID + ":" + ev.ProductID
	}
	switch {
	case ev.Product != "" && ids != "":
		return fmt.Sprintf("%s (%s)", ev.Product, ids)
	case ev.Product != "":
		return ev.Product
	case ids != "":
		return ids
	default:
		return "unknown device"
	}
}

// hardwareEventsResource returns the OTel resource for hardware event records.
// No hostname / instance id: device identity is added by the cloud ingestion
// path from the mTLS client cert, and locally the record is already scoped to
// the device being queried.
func hardwareEventsResource() *otelpb.Resource {
	return &otelpb.Resource{Attributes: []*otelpb.KeyValue{
		stringKV("service.name", hardwareEventsServiceName),
		stringKV("service.namespace", "wendy"),
	}}
}

// usbEventLogRecord builds the OTLP log request for one hotplug event.
// Connects are INFO; disconnects are WARN so a vanished peripheral surfaces in
// default INFO+ log views — on a headless edge device an unplug is rarely
// routine. Serial numbers are deliberately not included in streamed events
// (consistent with the dmesg PII stance); they remain available via the
// on-demand ListHardwareCapabilities RPC.
func usbEventLogRecord(resource *otelpb.Resource, ev usbEvent, now time.Time) *otelpb.ExportLogsServiceRequest {
	severity := otelpb.SeverityNumber_SEVERITY_NUMBER_INFO
	severityText := "INFO"
	if ev.Action == usbEventDisconnected {
		severity = otelpb.SeverityNumber_SEVERITY_NUMBER_WARN
		severityText = "WARN"
	}

	body := fmt.Sprintf("usb device %s: %s at %s", ev.Action, usbEventDisplayName(ev), ev.PortPath)
	attrs := []*otelpb.KeyValue{
		stringKV("wendy.hardware.subsystem", "usb"),
		stringKV("wendy.hardware.action", ev.Action),
		stringKV("wendy.hardware.port_path", ev.PortPath),
	}
	if ev.VendorID != "" {
		attrs = append(attrs, stringKV("wendy.hardware.vendor_id", ev.VendorID))
	}
	if ev.ProductID != "" {
		attrs = append(attrs, stringKV("wendy.hardware.product_id", ev.ProductID))
	}
	if ev.Product != "" {
		attrs = append(attrs, stringKV("wendy.hardware.product", ev.Product))
	}

	return singleLogRecordRequest(resource, "wendy.hardware", &otelpb.LogRecord{
		TimeUnixNano:         uint64(now.UnixNano()),
		ObservedTimeUnixNano: uint64(now.UnixNano()),
		SeverityNumber:       severity,
		SeverityText:         severityText,
		Body:                 &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
		Attributes:           attrs,
	})
}

// usbStormLogRecord reports events suppressed by the rate limiter during the
// previous one-second window. A re-enumeration storm is exactly the "unstable
// peripheral" signal this feature exists to surface, so suppression is
// published rather than only logged locally.
func usbStormLogRecord(resource *otelpb.Resource, dropped, forwarded int, now time.Time) *otelpb.ExportLogsServiceRequest {
	body := fmt.Sprintf("usb event storm: %d events suppressed in the last second (%d forwarded) — a device may be re-enumerating repeatedly", dropped, forwarded)
	return singleLogRecordRequest(resource, "wendy.hardware", &otelpb.LogRecord{
		TimeUnixNano:         uint64(now.UnixNano()),
		ObservedTimeUnixNano: uint64(now.UnixNano()),
		SeverityNumber:       otelpb.SeverityNumber_SEVERITY_NUMBER_WARN,
		SeverityText:         "WARN",
		Body:                 &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
		Attributes: []*otelpb.KeyValue{
			stringKV("wendy.hardware.subsystem", "usb"),
			stringKV("wendy.hardware.action", "storm"),
			stringKV("wendy.hardware.suppressed", strconv.Itoa(dropped)),
		},
	})
}

func singleLogRecordRequest(resource *otelpb.Resource, scope string, record *otelpb.LogRecord) *otelpb.ExportLogsServiceRequest {
	return &otelpb.ExportLogsServiceRequest{
		ResourceLogs: []*otelpb.ResourceLogs{{
			Resource: resource,
			ScopeLogs: []*otelpb.ScopeLogs{{
				Scope:      &otelpb.InstrumentationScope{Name: scope},
				LogRecords: []*otelpb.LogRecord{record},
			}},
		}},
	}
}
