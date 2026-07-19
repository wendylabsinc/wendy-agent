package services

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// Required-device alerting: apps declare the USB devices they need on their
// usb entitlement (wendy.json `devices: [{vendorId, productId}]`); this
// reconciler diffs those declarations against the devices actually on the bus
// and publishes required_missing / required_restored events into the same
// wendy.hardware telemetry stream as the hotplug events. A silent USB drop
// thereby becomes "app X's CAN adapter is gone", not an inference from app
// failures (WDY-1923).

// requiredDeviceReconcileInterval is the periodic fallback cadence. Hotplug
// events additionally trigger an immediate round via the trigger channel, so
// this only bounds detection latency for changes with no uevent (e.g. an app
// with requirements starting or stopping).
const requiredDeviceReconcileInterval = 30 * time.Second

// USBRequirementSource lists the USB devices currently-running apps declare
// they need. Implemented by the containerd client from entitlement container
// labels; an optional capability so the wide ContainerdClient interface and
// its mocks stay untouched (same pattern as GroupRestarter).
type USBRequirementSource interface {
	RequiredUSBDevices(ctx context.Context) (map[string][]appconfig.USBDeviceMatcher, error)
}

// CollectRequiredDeviceAlerts reconciles declared vs present USB devices until
// ctx is cancelled. trigger (may be nil) requests an immediate round; the
// hotplug collector signals it on every device add/remove.
func CollectRequiredDeviceAlerts(
	ctx context.Context,
	logger *zap.Logger,
	publisher TelemetryPublisher,
	source USBRequirementSource,
	trigger <-chan struct{},
) {
	resource := hardwareEventsResource()
	state := make(map[requiredDeviceKey]bool)

	round := func(now time.Time) {
		required, err := source.RequiredUSBDevices(ctx)
		if err != nil {
			logger.Debug("required-device reconcile: listing requirements failed", zap.Error(err))
			return
		}
		present, err := usbPresentDevices()
		if err != nil {
			logger.Debug("required-device reconcile: presence scan unavailable", zap.Error(err))
			return
		}
		for _, req := range reconcileRequiredDevices(state, required, present) {
			publisher.PublishLogs(requiredDeviceLogRecord(resource, req, now))
			logger.Warn("required usb device state change",
				zap.String("app", req.AppID),
				zap.String("device", req.Device),
				zap.Bool("missing", req.Missing),
			)
		}
	}

	logger.Info("required-device alerting started")
	defer logger.Info("required-device alerting stopped")

	ticker := time.NewTicker(requiredDeviceReconcileInterval)
	defer ticker.Stop()
	round(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			round(time.Now())
		case t := <-ticker.C:
			round(t)
		}
	}
}

type requiredDeviceKey struct {
	appID  string
	device string // "vvvv:pppp"
}

// requiredDeviceChange is one alert-worthy transition of a declared device.
type requiredDeviceChange struct {
	AppID   string
	Device  string // "vvvv:pppp"
	Missing bool
}

// reconcileRequiredDevices diffs required against present and mutates state,
// returning the transitions to publish. Rules:
//   - a requirement first seen while missing alerts immediately (an app whose
//     device was already gone at agent startup must not be silent);
//   - a requirement first seen while present records silently;
//   - subsequent present/missing flips alert on every transition;
//   - requirements that disappear (app stopped) are forgotten without a
//     "restored" event.
//
// Deterministic output order (by app, then device) keeps the event stream and
// tests stable.
func reconcileRequiredDevices(
	state map[requiredDeviceKey]bool,
	required map[string][]appconfig.USBDeviceMatcher,
	present map[string]bool,
) []requiredDeviceChange {
	var changes []requiredDeviceChange

	active := make(map[requiredDeviceKey]bool)
	appIDs := make([]string, 0, len(required))
	for appID := range required {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)

	for _, appID := range appIDs {
		for _, m := range required[appID] {
			key := requiredDeviceKey{appID: appID, device: m.String()}
			if active[key] {
				continue
			}
			active[key] = true

			isPresent := present[key.device]
			was, seen := state[key]
			state[key] = isPresent
			switch {
			case !seen && !isPresent:
				changes = append(changes, requiredDeviceChange{AppID: appID, Device: key.device, Missing: true})
			case seen && was != isPresent:
				changes = append(changes, requiredDeviceChange{AppID: appID, Device: key.device, Missing: !isPresent})
			}
		}
	}

	for key := range state {
		if !active[key] {
			delete(state, key)
		}
	}
	return changes
}

// requiredDeviceLogRecord builds the OTLP record for one transition. Missing
// is ERROR — an app's declared hardware dependency is unmet — and restored is
// INFO.
func requiredDeviceLogRecord(resource *otelpb.Resource, ch requiredDeviceChange, now time.Time) *otelpb.ExportLogsServiceRequest {
	action := "required_restored"
	severity := otelpb.SeverityNumber_SEVERITY_NUMBER_INFO
	severityText := "INFO"
	verb := "restored"
	if ch.Missing {
		action = "required_missing"
		severity = otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR
		severityText = "ERROR"
		verb = "missing"
	}

	vendorID, productID := "", ""
	if len(ch.Device) == 9 { // "vvvv:pppp"
		vendorID, productID = ch.Device[:4], ch.Device[5:]
	}
	attrs := []*otelpb.KeyValue{
		stringKV("wendy.hardware.subsystem", "usb"),
		stringKV("wendy.hardware.action", action),
		stringKV("wendy.hardware.app", ch.AppID),
	}
	if vendorID != "" {
		attrs = append(attrs,
			stringKV("wendy.hardware.vendor_id", vendorID),
			stringKV("wendy.hardware.product_id", productID),
		)
	}

	body := fmt.Sprintf("required usb device %s: %s (required by app %s)", verb, ch.Device, ch.AppID)
	return singleLogRecordRequest(resource, "wendy.hardware", &otelpb.LogRecord{
		TimeUnixNano:         uint64(now.UnixNano()),
		ObservedTimeUnixNano: uint64(now.UnixNano()),
		SeverityNumber:       severity,
		SeverityText:         severityText,
		Body:                 &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
		Attributes:           attrs,
	})
}
