package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// fakeBackend is a scripted LAN stream backend: the test pushes services onto
// ch and the backend hands them to the engine, staying alive until ctx ends.
type fakeBackend struct{ ch chan MDNSService }

func newFakeBackend() *fakeBackend { return &fakeBackend{ch: make(chan MDNSService)} }

func (f *fakeBackend) fn(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case svc := <-f.ch:
			emit(svc)
		}
	}
}

// emit pushes svc to the backend, failing the test if the engine never picks
// it up (a hung session would otherwise deadlock the test).
func (f *fakeBackend) emit(t *testing.T, svc MDNSService) {
	t.Helper()
	select {
	case f.ch <- svc:
	case <-time.After(2 * time.Second):
		t.Fatalf("backend emit blocked: %+v", svc)
	}
}

// useStreamSeams points the engine at a fake backend and cache loader for the
// duration of the test. Call it before starting a session so the restore
// runs after the session has been torn down (t.Cleanup is LIFO).
func useStreamSeams(t *testing.T, backend func(context.Context, string, func(MDNSService)) error, load func() (*discoverycache.Cache, error)) {
	t.Helper()
	origBackend, origLoad := lanBackendFn, cacheLoadFn
	t.Cleanup(func() { lanBackendFn, cacheLoadFn = origBackend, origLoad })
	if backend != nil {
		lanBackendFn = backend
	}
	if load != nil {
		cacheLoadFn = load
	}
}

// shrinkDuration retunes one of the engine's timing knobs for this test.
func shrinkDuration(t *testing.T, knob *time.Duration, d time.Duration) {
	t.Helper()
	orig := *knob
	t.Cleanup(func() { *knob = orig })
	*knob = d
}

// cacheLoaderFor returns a cacheLoadFn that reads the cache file at path.
func cacheLoaderFor(path string) func() (*discoverycache.Cache, error) {
	return func() (*discoverycache.Cache, error) { return discoverycache.LoadFrom(path) }
}

// seedCache writes entries to a fresh cache file at path, stamped now.
func seedCache(t *testing.T, path string, entries ...discoverycache.Entry) {
	t.Helper()
	c, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("seed load: %v", err)
	}
	now := time.Now()
	for _, e := range entries {
		c.Upsert(e, now)
	}
	if err := c.Flush(now); err != nil {
		t.Fatalf("seed flush: %v", err)
	}
}

// startStream runs a session and returns its event channel plus a stop func
// that cancels it and drains the channel until the engine has fully shut
// down. stop also runs on cleanup, so the timing/seam overrides above are
// never restored while a session is still reading them.
func startStream(t *testing.T, opts StreamOptions) (<-chan LANEvent, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	events := StreamLAN(ctx, opts)
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			for range events {
			}
		})
	}
	t.Cleanup(stop)
	return events, stop
}

// collectEvents reads exactly n events or fails.
func collectEvents(t *testing.T, ch <-chan LANEvent, n int, timeout time.Duration) []LANEvent {
	t.Helper()
	events := make([]LANEvent, 0, n)
	deadline := time.After(timeout)
	for len(events) < n {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed after %d of %d events: %+v", len(events), n, events)
			}
			events = append(events, ev)
		case <-deadline:
			t.Fatalf("timed out after %d of %d events: %+v", len(events), n, events)
		}
	}
	return events
}

// expectQuiet fails if any further event arrives within d.
func expectQuiet(t *testing.T, ch <-chan LANEvent, d time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("unexpected event: %+v", ev)
		}
		t.Fatal("stream closed unexpectedly")
	case <-time.After(d):
	}
}

// wendyService builds a resolved WendyOS mDNS service entry.
func wendyService(id, display, hostname, ip string, port int) MDNSService {
	return MDNSService{
		InstanceName: display,
		Hostname:     hostname,
		IPAddress:    ip,
		Port:         port,
		TXTRecords: map[string]string{
			"wendyosdevice": id,
			"displayname":   display,
		},
	}
}

func failingProber(err error) LANProber {
	return func(context.Context, models.LANDevice) (models.LANDevice, error) {
		return models.LANDevice{}, err
	}
}

func TestStreamCachedThenProbeConfirms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	fb := newFakeBackend() // stays silent
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	prober := func(_ context.Context, dev models.LANDevice) (models.LANDevice, error) {
		dev.AgentVersion = "9.9.9"
		dev.IsMTLS = true
		return dev, nil
	}

	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: prober})

	got := collectEvents(t, events, 2, 5*time.Second)
	first, second := got[0], got[1]
	if first.Kind != LANCached || first.Device.ID != "dev-1" || first.Probed {
		t.Fatalf("first event must be the cached row: %+v", first)
	}
	if second.Kind != LANFound || !second.Probed || second.Device.AgentVersion != "9.9.9" || !second.Device.IsMTLS {
		t.Fatalf("second event must be the probe confirmation: %+v", second)
	}
	if second.Device.ID != "dev-1" || second.Device.IPAddress != "10.0.0.5" {
		t.Fatalf("probe confirmation must keep the cached identity/address: %+v", second.Device)
	}
}

func TestStreamCachedProbeFailsThenOffline(t *testing.T) {
	grace := 150 * time.Millisecond
	shrinkDuration(t, &offlineGrace, grace)

	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	fb := newFakeBackend() // stays silent: nothing on the network
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	start := time.Now()
	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: failingProber(errors.New("dial tcp: connection refused"))})

	got := collectEvents(t, events, 2, 5*time.Second)
	if got[0].Kind != LANCached || got[0].Device.ID != "dev-1" {
		t.Fatalf("first event must be the cached row: %+v", got[0])
	}
	if got[1].Kind != LANOffline || got[1].Device.ID != "dev-1" || got[1].Probed {
		t.Fatalf("second event must mark the same device offline: %+v", got[1])
	}
	// The probe fails immediately; offline still waits out the grace window
	// (the session timer starts after `start`, so this cannot false-fail).
	if elapsed := time.Since(start); elapsed < grace {
		t.Fatalf("offline marker fired %v after start, before the %v grace window", elapsed, grace)
	}
	// A failed probe never produces a confirmation, and the offline marker is
	// emitted once — the next re-probe is offlineRetryDelay away.
	expectQuiet(t, events, 200*time.Millisecond)
}

func TestStreamCachedPendingProbeStaysOnlinePastGrace(t *testing.T) {
	shrinkDuration(t, &offlineGrace, 50*time.Millisecond)

	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	// A slow agent: the probe is still in flight when the grace window ends.
	slowProber := func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		<-ctx.Done()
		return models.LANDevice{}, ctx.Err()
	}

	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: slowProber})

	if got := collectEvents(t, events, 1, 5*time.Second)[0]; got.Kind != LANCached {
		t.Fatalf("first event must be the cached row: %+v", got)
	}
	// Offline needs a *failed* probe, not just an elapsed grace window.
	expectQuiet(t, events, 250*time.Millisecond)
}

func TestStreamOfflineDeviceReturnsViaMDNS(t *testing.T) {
	shrinkDuration(t, &offlineGrace, 50*time.Millisecond)

	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: failingProber(errors.New("no route to host"))})

	got := collectEvents(t, events, 2, 5*time.Second)
	if got[0].Kind != LANCached || got[1].Kind != LANOffline {
		t.Fatalf("expected cached then offline, got %+v", got)
	}

	// The device shows up on the network at a new address.
	fb.emit(t, wendyService("dev-1", "orin", "orin.local", "10.0.0.9", 50051))

	back := collectEvents(t, events, 1, 5*time.Second)[0]
	if back.Kind != LANFound || back.Device.ID != "dev-1" || back.Device.IPAddress != "10.0.0.9" {
		t.Fatalf("mDNS sighting must flip the device back online: %+v", back)
	}
	if back.Probed {
		t.Fatalf("mDNS confirmation is not a probe confirmation: %+v", back)
	}
}

func TestStreamProbeRetargetsOnAddressChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	// The probe against the stale address is held open until the test
	// releases it, long after the device has moved.
	release := make(chan struct{})
	prober := func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error) {
		if dev.IPAddress == "10.0.0.5" {
			select {
			case <-release:
			case <-ctx.Done():
				return models.LANDevice{}, ctx.Err()
			}
			dev.AgentVersion = "stale"
			return dev, nil
		}
		dev.AgentVersion = "current"
		return dev, nil
	}

	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: prober})
	if got := collectEvents(t, events, 1, 5*time.Second)[0]; got.Kind != LANCached {
		t.Fatalf("first event must be the cached row: %+v", got)
	}

	fb.emit(t, wendyService("dev-1", "orin", "orin.local", "10.0.0.9", 50051))

	got := collectEvents(t, events, 2, 5*time.Second)
	if got[0].Kind != LANFound || got[0].Device.IPAddress != "10.0.0.9" {
		t.Fatalf("mDNS sighting at the new address must confirm the row: %+v", got[0])
	}
	if got[1].Kind != LANUpdated || !got[1].Probed || got[1].Device.AgentVersion != "current" {
		t.Fatalf("the retargeted probe must confirm the new address: %+v", got[1])
	}

	// The in-flight probe against the old address finally answers: its result
	// no longer speaks for this device and must be discarded.
	close(release)
	expectQuiet(t, events, 300*time.Millisecond)
}

func TestStreamNewDeviceFoundThenProbedUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json") // no file: empty cache

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	prober := func(_ context.Context, dev models.LANDevice) (models.LANDevice, error) {
		dev.AgentVersion = "1.2.3"
		dev.OS = "WendyOS"
		return dev, nil
	}

	events, _ := startStream(t, StreamOptions{UseCache: true, Prober: prober})
	fb.emit(t, wendyService("dev-2", "pi", "pi.local", "10.0.0.7", 50051))

	got := collectEvents(t, events, 2, 5*time.Second)
	if got[0].Kind != LANFound || got[0].Probed || got[0].Device.ID != "dev-2" || got[0].Device.AgentVersion != "" {
		t.Fatalf("first event must be the unprobed mDNS sighting: %+v", got[0])
	}
	if got[1].Kind != LANUpdated || !got[1].Probed || got[1].Device.AgentVersion != "1.2.3" || got[1].Device.OS != "WendyOS" {
		t.Fatalf("second event must be the probe update: %+v", got[1])
	}
}

func TestStreamDedupAndAddressChange(t *testing.T) {
	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, nil)

	events, _ := startStream(t, StreamOptions{}) // no cache, no prober

	svc := wendyService("dev-3", "nano", "nano.local", "10.0.0.3", 50051)
	fb.emit(t, svc)
	fb.emit(t, svc) // identical re-announcement: must not produce an event

	found := collectEvents(t, events, 1, 5*time.Second)[0]
	if found.Kind != LANFound || found.Device.IPAddress != "10.0.0.3" {
		t.Fatalf("first event must be the initial sighting: %+v", found)
	}

	moved := svc
	moved.IPAddress = "10.0.0.44"
	fb.emit(t, moved)

	// If the duplicate had been emitted it would arrive here instead.
	next := collectEvents(t, events, 1, 5*time.Second)[0]
	if next.Kind != LANUpdated || next.Device.IPAddress != "10.0.0.44" || next.Device.ID != "dev-3" {
		t.Fatalf("address change must produce a single update: %+v", next)
	}
	expectQuiet(t, events, 100*time.Millisecond)
}

func TestStreamNoCacheNoCachedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-1", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.5", Port: 50051})

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read seeded cache: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat seeded cache: %v", err)
	}

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	events, stop := startStream(t, StreamOptions{UseCache: false})
	fb.emit(t, wendyService("dev-9", "other", "other.local", "10.0.0.9", 50051))

	got := collectEvents(t, events, 1, 5*time.Second)[0]
	if got.Kind != LANFound || got.Device.ID != "dev-9" {
		t.Fatalf("without UseCache the first event must be a live sighting: %+v", got)
	}
	stop()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache after session: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("cache file was rewritten without UseCache:\nbefore: %s\nafter:  %s", before, after)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache after session: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("cache file mtime changed without UseCache: %v → %v", beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

func TestStreamConfirmedOnlyUpsertsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	events, stop := startStream(t, StreamOptions{UseCache: true}) // browse-only, no prober
	fb.emit(t, wendyService("dev-4", "jetson", "jetson.local", "10.0.0.4", 50051))

	got := collectEvents(t, events, 1, 5*time.Second)[0]
	if got.Kind != LANFound {
		t.Fatalf("expected a live sighting: %+v", got)
	}
	stop() // session end flushes the cache before the channel closes

	reloaded, err := discoverycache.LoadFrom(path)
	if err != nil {
		t.Fatalf("reload cache: %v", err)
	}
	fresh := reloaded.Fresh(time.Now())
	if len(fresh) != 1 {
		t.Fatalf("expected exactly one persisted entry, got %+v", fresh)
	}
	entry := fresh[0]
	if entry.ID != "dev-4" || entry.Hostname != "jetson.local" || entry.IP != "10.0.0.4" || entry.Port != 50051 {
		t.Fatalf("browse-only upsert did not persist the device: %+v", entry)
	}
	if time.Since(entry.LastSeen) > time.Minute {
		t.Fatalf("LastSeen must be stamped at sighting time: %v", entry.LastSeen)
	}
}

func TestStreamPeriodicFlushPersistsMidSession(t *testing.T) {
	shrinkDuration(t, &cacheFlushDelay, 20*time.Millisecond)

	path := filepath.Join(t.TempDir(), "devices.json")

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	events, _ := startStream(t, StreamOptions{UseCache: true})
	fb.emit(t, wendyService("dev-10", "orin", "orin.local", "10.0.0.10", 50051))
	if got := collectEvents(t, events, 1, 5*time.Second)[0]; got.Kind != LANFound {
		t.Fatalf("expected a live sighting: %+v", got)
	}

	// A long-lived session (picker, discover TUI) must not hold its findings
	// until shutdown — the debounced flush ticker writes them out as it goes.
	deadline := time.Now().Add(3 * time.Second)
	for {
		cache, err := discoverycache.LoadFrom(path)
		if err != nil {
			t.Fatalf("reload cache: %v", err)
		}
		if fresh := cache.Fresh(time.Now()); len(fresh) == 1 && fresh[0].ID == "dev-10" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("device never reached the cache file while the session was still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamBackendRestarts(t *testing.T) {
	shrinkDuration(t, &backendRetryDelay, 10*time.Millisecond)

	var calls atomic.Int32
	backend := func(ctx context.Context, _ string, emit func(MDNSService)) error {
		switch calls.Add(1) {
		case 1:
			emit(wendyService("dev-5", "first", "first.local", "10.0.0.5", 50051))
			return errors.New("mDNSResponder went away")
		default:
			emit(wendyService("dev-6", "second", "second.local", "10.0.0.6", 50051))
			<-ctx.Done()
			return nil
		}
	}
	useStreamSeams(t, backend, nil)

	events, _ := startStream(t, StreamOptions{})

	got := collectEvents(t, events, 2, 5*time.Second)
	if got[0].Kind != LANFound || got[0].Device.ID != "dev-5" {
		t.Fatalf("first event must come from the first backend run: %+v", got[0])
	}
	if got[1].Kind != LANFound || got[1].Device.ID != "dev-6" {
		t.Fatalf("second event must come from the restarted backend: %+v", got[1])
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("backend must be invoked exactly twice, got %d", n)
	}
}

func TestStreamCacheLoadErrorTreatedAsEmpty(t *testing.T) {
	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, func() (*discoverycache.Cache, error) {
		return nil, errors.New("looking up home directory: no $HOME")
	})

	events, stop := startStream(t, StreamOptions{UseCache: true, Prober: failingProber(errors.New("unreachable"))})
	fb.emit(t, wendyService("dev-7", "orin", "orin.local", "10.0.0.7", 50051))

	got := collectEvents(t, events, 1, 5*time.Second)[0]
	if got.Kind != LANFound || got.Device.ID != "dev-7" {
		t.Fatalf("a failed cache load must degrade to an empty cache, not to no scan: %+v", got)
	}
	stop() // the session-end flush must not panic without a cache
}

func TestRunLANStreamClosesProbesDone(t *testing.T) {
	shrinkDuration(t, &offlineGrace, 50*time.Millisecond)

	path := filepath.Join(t.TempDir(), "devices.json")
	seedCache(t, path, discoverycache.Entry{ID: "dev-8", DisplayName: "orin", Hostname: "orin.local", IP: "10.0.0.8", Port: 50051})

	fb := newFakeBackend()
	useStreamSeams(t, fb.fn, cacheLoaderFor(path))

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan LANEvent, 8)
	probesDone := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(out)
		runLANStream(ctx, StreamOptions{UseCache: true, Prober: failingProber(errors.New("unreachable"))}, out, probesDone)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	select {
	case <-probesDone:
	case <-time.After(5 * time.Second):
		t.Fatal("probesDone must close once every cached entry's initial probe concluded")
	}
}
