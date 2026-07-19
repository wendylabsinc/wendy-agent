package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// Device-local USB watch list (WDY-1923). The list is set interactively from
// the CLI (`wendy device hardware watch`) and stored on the device — nothing
// in wendy.json. The agent alerts when a watched device is absent beyond a
// grace period: watched_missing (ERROR) into the wendy.hardware telemetry
// stream, watched_restored (INFO) when it returns. Watches may pin a serial
// number so two identical adapters are tracked individually; a watch without
// a serial matches any device with that vendor:product id.

const (
	// hardwareWatchStorePath persists the watch list across agent restarts.
	// Lives next to the telemetry buffer, which is already agent-writable.
	hardwareWatchStorePath = "/var/lib/wendy-agent/hardware-watch.json"

	// watchedDeviceGracePeriod is how long a watched device must be absent
	// before the missing alert fires. Long enough that a quick replug or
	// re-enumeration doesn't alarm; short enough to beat a human noticing arm
	// faults.
	watchedDeviceGracePeriod = 30 * time.Second

	// watchedDeviceTickInterval bounds alert latency between hotplug triggers.
	watchedDeviceTickInterval = 10 * time.Second
)

// WatchedDevice is one entry of the device's hardware watch list.
type WatchedDevice struct {
	VendorID  string `json:"vendorId"`
	ProductID string `json:"productId"`
	Serial    string `json:"serial,omitempty"`
	Label     string `json:"label,omitempty"`
}

// Key renders the stable identity of the watch ("vvvv:pppp" or
// "vvvv:pppp serial").
func (w WatchedDevice) Key() string {
	k := strings.ToLower(w.VendorID) + ":" + strings.ToLower(w.ProductID)
	if w.Serial != "" {
		k += " " + w.Serial
	}
	return k
}

// DisplayName prefers the label captured at selection time.
func (w WatchedDevice) DisplayName() string {
	if w.Label != "" {
		return fmt.Sprintf("%s (%s)", w.Label, w.Key())
	}
	return w.Key()
}

// ValidateWatchedDevice checks ids are 4-digit hex; serial and label are free-form.
func ValidateWatchedDevice(w WatchedDevice) error {
	if !isHex4(w.VendorID) || !isHex4(w.ProductID) {
		return fmt.Errorf("watched device requires 4-digit hex vendor and product ids, got %q:%q", w.VendorID, w.ProductID)
	}
	return nil
}

func isHex4(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// HardwareWatchStore is the JSON-file-backed watch list. Save nudges the
// notify channel (when set) so the alert loop reconciles immediately.
type HardwareWatchStore struct {
	mu     sync.Mutex
	path   string
	notify chan<- struct{}
}

func NewHardwareWatchStore(path string, notify chan<- struct{}) *HardwareWatchStore {
	if path == "" {
		path = hardwareWatchStorePath
	}
	return &HardwareWatchStore{path: path, notify: notify}
}

// Load returns the stored watch list; a missing file is an empty list.
func (s *HardwareWatchStore) Load() ([]WatchedDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var devices []WatchedDevice
	if err := json.Unmarshal(data, &devices); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", s.path, err)
	}
	return devices, nil
}

// Save atomically replaces the watch list (write temp + rename).
func (s *HardwareWatchStore) Save(devices []WatchedDevice) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(devices, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	if s.notify != nil {
		select {
		case s.notify <- struct{}{}:
		default:
		}
	}
	return nil
}

// presentUSBDevice is one device currently on the bus, as seen by the
// serial-aware presence scan (usbPresentDetail, per-platform).
type presentUSBDevice struct {
	VendorID  string // 4-digit lowercase hex
	ProductID string // 4-digit lowercase hex
	Serial    string // may be empty
}

// watchState tracks one watch's absence episode.
type watchState struct {
	missingSince time.Time // zero when present
	alerted      bool      // missing alert published for the current episode
}

// watchedChange is one alert-worthy transition of a watched device.
type watchedChange struct {
	Watch   WatchedDevice
	Missing bool
}

// CollectWatchedDeviceAlerts reconciles the watch list against the bus until
// ctx is cancelled. trigger (may be nil) requests an immediate round; the
// hotplug collector and the watch store both signal it.
func CollectWatchedDeviceAlerts(
	ctx context.Context,
	logger *zap.Logger,
	publisher TelemetryPublisher,
	store *HardwareWatchStore,
	trigger <-chan struct{},
) {
	resource := hardwareEventsResource()
	state := make(map[string]*watchState)

	round := func(now time.Time) {
		watches, err := store.Load()
		if err != nil {
			logger.Warn("hardware watch: loading watch list failed", zap.Error(err))
			return
		}
		present, err := usbPresentDetail()
		if err != nil {
			logger.Debug("hardware watch: presence scan unavailable", zap.Error(err))
			return
		}
		for _, ch := range reconcileWatchedDevices(state, watches, present, now, watchedDeviceGracePeriod) {
			publisher.PublishLogs(watchedDeviceLogRecord(resource, ch, now))
			logger.Warn("watched usb device state change",
				zap.String("device", ch.Watch.Key()),
				zap.Bool("missing", ch.Missing),
			)
		}
	}

	logger.Info("hardware watch alerting started")
	defer logger.Info("hardware watch alerting stopped")

	ticker := time.NewTicker(watchedDeviceTickInterval)
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

// reconcileWatchedDevices diffs watches against present devices, mutating
// state and returning transitions to publish. A watch alerts once per absence
// episode, after the device has been missing for at least grace; the restored
// event fires only if the missing alert fired. Watches removed from the list
// are forgotten silently. Output order follows the (sorted) watch keys.
func reconcileWatchedDevices(
	state map[string]*watchState,
	watches []WatchedDevice,
	present []presentUSBDevice,
	now time.Time,
	grace time.Duration,
) []watchedChange {
	var changes []watchedChange

	sorted := make([]WatchedDevice, len(watches))
	copy(sorted, watches)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })

	active := make(map[string]bool, len(sorted))
	for _, w := range sorted {
		key := w.Key()
		if active[key] {
			continue
		}
		active[key] = true

		st := state[key]
		if st == nil {
			st = &watchState{}
			state[key] = st
		}

		if watchMatchesAny(w, present) {
			if st.alerted {
				changes = append(changes, watchedChange{Watch: w, Missing: false})
			}
			st.missingSince = time.Time{}
			st.alerted = false
			continue
		}

		if st.missingSince.IsZero() {
			st.missingSince = now
		}
		if !st.alerted && now.Sub(st.missingSince) >= grace {
			st.alerted = true
			changes = append(changes, watchedChange{Watch: w, Missing: true})
		}
	}

	for key := range state {
		if !active[key] {
			delete(state, key)
		}
	}
	return changes
}

func watchMatchesAny(w WatchedDevice, present []presentUSBDevice) bool {
	v := strings.ToLower(w.VendorID)
	p := strings.ToLower(w.ProductID)
	for _, d := range present {
		if d.VendorID != v || d.ProductID != p {
			continue
		}
		if w.Serial == "" || w.Serial == d.Serial {
			return true
		}
	}
	return false
}

// watchedDeviceLogRecord builds the OTLP record for one watch transition.
func watchedDeviceLogRecord(resource *otelpb.Resource, ch watchedChange, now time.Time) *otelpb.ExportLogsServiceRequest {
	action := "watched_restored"
	severity := otelpb.SeverityNumber_SEVERITY_NUMBER_INFO
	severityText := "INFO"
	verb := "restored"
	if ch.Missing {
		action = "watched_missing"
		severity = otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR
		severityText = "ERROR"
		verb = "missing"
	}

	attrs := []*otelpb.KeyValue{
		stringKV("wendy.hardware.subsystem", "usb"),
		stringKV("wendy.hardware.action", action),
		stringKV("wendy.hardware.vendor_id", strings.ToLower(ch.Watch.VendorID)),
		stringKV("wendy.hardware.product_id", strings.ToLower(ch.Watch.ProductID)),
	}
	if ch.Watch.Serial != "" {
		attrs = append(attrs, stringKV("wendy.hardware.serial", ch.Watch.Serial))
	}
	if ch.Watch.Label != "" {
		attrs = append(attrs, stringKV("wendy.hardware.product", ch.Watch.Label))
	}

	body := fmt.Sprintf("watched usb device %s: %s", verb, ch.Watch.DisplayName())
	return singleLogRecordRequest(resource, "wendy.hardware", &otelpb.LogRecord{
		TimeUnixNano:         uint64(now.UnixNano()),
		ObservedTimeUnixNano: uint64(now.UnixNano()),
		SeverityNumber:       severity,
		SeverityText:         severityText,
		Body:                 &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: body}},
		Attributes:           attrs,
	})
}
