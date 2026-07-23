package services

import (
	"sync"
	"time"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// HardwareEventHub fans hardware events out to live WatchHardware subscribers.
// The hotplug collector and the watch alert loop publish here in addition to
// the telemetry pipeline: telemetry is the durable/history path, the hub is
// the real-time push path (no polling — see WatchHardware in
// device_info_service.proto).
type HardwareEventHub struct {
	mu   sync.Mutex
	subs map[chan *agentpbv2.HardwareEvent]struct{}
}

// hardwareHubSubBuffer bounds each subscriber's queue. A subscriber that
// stalls past it (dead tunnel, saturated link) loses events rather than
// blocking the collectors; the durable history stays in telemetry.
const hardwareHubSubBuffer = 64

func NewHardwareEventHub() *HardwareEventHub {
	return &HardwareEventHub{subs: make(map[chan *agentpbv2.HardwareEvent]struct{})}
}

// Subscribe registers a listener. The returned cancel must be called when the
// subscriber goes away; it is idempotent.
func (h *HardwareEventHub) Subscribe() (<-chan *agentpbv2.HardwareEvent, func()) {
	ch := make(chan *agentpbv2.HardwareEvent, hardwareHubSubBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish delivers ev to every subscriber, dropping it for subscribers whose
// buffer is full. Nil-receiver safe so collectors can be wired without a hub.
func (h *HardwareEventHub) Publish(ev *agentpbv2.HardwareEvent) {
	if h == nil || ev == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// usbEventProto converts a hotplug transition to the WatchHardware wire event.
// Serials are deliberately not part of hotplug events (PII stance); watch
// alerts carry them because pinning is their point.
func usbEventProto(ev usbEvent, now time.Time) *agentpbv2.HardwareEvent {
	return &agentpbv2.HardwareEvent{
		Action:       ev.Action,
		TimeUnixNano: now.UnixNano(),
		Message:      "usb device " + ev.Action + ": " + usbEventDisplayName(ev) + " at " + ev.PortPath,
		VendorId:     ev.VendorID,
		ProductId:    ev.ProductID,
		Product:      ev.Product,
		PortPath:     ev.PortPath,
	}
}

func usbStormProto(dropped, forwarded int, now time.Time) *agentpbv2.HardwareEvent {
	rec := usbStormLogRecord(hardwareEventsResource(), dropped, forwarded, now)
	return &agentpbv2.HardwareEvent{
		Action:       "storm",
		TimeUnixNano: now.UnixNano(),
		Message:      rec.ResourceLogs[0].ScopeLogs[0].LogRecords[0].GetBody().GetStringValue(),
		Suppressed:   int32(dropped),
	}
}

func watchedChangeProto(ch watchedChange, now time.Time) *agentpbv2.HardwareEvent {
	rec := watchedDeviceLogRecord(hardwareEventsResource(), ch, now)
	action := "watched_restored"
	if ch.Missing {
		action = "watched_missing"
	}
	return &agentpbv2.HardwareEvent{
		Action:       action,
		TimeUnixNano: now.UnixNano(),
		Message:      rec.ResourceLogs[0].ScopeLogs[0].LogRecords[0].GetBody().GetStringValue(),
		VendorId:     ch.Watch.VendorID,
		ProductId:    ch.Watch.ProductID,
		Serial:       ch.Watch.Serial,
		Product:      ch.Watch.Label,
	}
}
