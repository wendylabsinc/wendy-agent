package discovery

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/discoverycache"
	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

// LANEventKind classifies a LANEvent emitted by a streaming LAN discovery scan.
type LANEventKind int

const (
	LANCached  LANEventKind = iota // cache entry, not yet verified this run
	LANFound                       // live-confirmed (mDNS resolve or probe)
	LANUpdated                     // an already-emitted device changed
	LANOffline                     // cached entry failed verification
)

// LANEvent is a single update emitted while streaming LAN discovery results.
type LANEvent struct {
	Kind   LANEventKind
	Device models.LANDevice
	// Probed: Device's AgentVersion/OS/IsMTLS were confirmed by a live agent
	// probe (not just mDNS TXT records).
	Probed bool
}

// LANProber verifies a device by talking to its agent. On success the
// returned device carries refreshed AgentVersion/DeviceType/OS/OSVersion/
// CPUArchitecture and IsMTLS reflecting the actual connection.
type LANProber func(ctx context.Context, dev models.LANDevice) (models.LANDevice, error)

// StreamOptions configures a streaming LAN discovery scan.
type StreamOptions struct {
	UseCache bool      // emit cached entries and persist discoveries
	Prober   LANProber // nil = no probing (mDNS-only confirmation)
}

// Package seams. These are vars, not consts, so tests can swap the backend and
// the cache location and shrink the timings; production never reassigns them.
//
// A lanBackendFn implementation browses serviceType and calls emit for every
// resolved answer. It MUST return once ctx is done — the session waits for it
// before closing its event channel — and it may return a non-nil error at any
// time to have the session restart it (see runBackend).
var (
	lanBackendFn      = mdnsStreamBackend       // per-platform mDNS stream
	cacheLoadFn       = discoverycache.Load     // recently-seen device cache
	offlineGrace      = 4 * time.Second         // cached & silent → Offline
	offlineRetryDelay = 30 * time.Second        // one re-probe after Offline
	probeTimeout      = 1500 * time.Millisecond // per-probe ctx budget
	probeWorkers      = 4                       // concurrent probes/resolves
	cacheFlushDelay   = time.Second             // debounce for cache writes
	backendRetryDelay = 2 * time.Second         // backend died mid-session
	backendRetries    = 3                       // ...restart attempts before giving up
)

// streamEventBuffer decouples the engine loop from a consumer that renders
// between reads. Events are never dropped while the session is live; the loop
// simply blocks once the buffer fills.
const streamEventBuffer = 32

// mdnsStreamBackend is the placeholder LAN stream backend: it produces no
// sightings and returns when ctx ends. Tasks 5-7 replace it with the
// per-platform implementations (darwin DNSSD, linux Avahi/D-Bus, windows
// hashicorp-mdns); it is deliberately build-tag-free so every GOOS compiles
// until then.
func mdnsStreamBackend(ctx context.Context, serviceType string, emit func(MDNSService)) error {
	<-ctx.Done()
	return nil
}

// StreamLAN emits fresh cache entries immediately (when opts.UseCache), then
// live results and probe outcomes until ctx is cancelled. The channel closes
// when the session ends.
func StreamLAN(ctx context.Context, opts StreamOptions) <-chan LANEvent {
	out := make(chan LANEvent, streamEventBuffer)
	go func() {
		defer close(out)
		runLANStream(ctx, opts, out, nil)
	}()
	return out
}

// lanDeviceState is the engine's bookkeeping for one device identity. It is
// owned exclusively by runLANStream's goroutine, so it carries no locks.
type lanDeviceState struct {
	dev models.LANDevice
	// fromCache: this identity entered the session as a LANCached row, so it
	// is the only kind that can go LANOffline.
	fromCache bool
	// confirmed: a live confirmation (mDNS sighting or probe success) has
	// been emitted for this identity.
	confirmed bool
	// probeConfirmed: a probe has succeeded at least once, so the device's
	// version fields are agent-verified rather than cache- or TXT-derived.
	probeConfirmed bool
	probing        bool
	probeFailed    bool
	// probeGen rises whenever a probe is (re)scheduled; a result carrying an
	// older generation lost its target and is discarded.
	probeGen       int
	offline        bool
	retryScheduled bool
}

// lanProbeResult is one prober outcome handed back to the engine loop.
type lanProbeResult struct {
	key string
	gen int
	dev models.LANDevice
	err error
}

// lanStream holds one session's state. Every field is read and written on the
// runLANStream goroutine except the channels and the WaitGroup.
type lanStream struct {
	ctx  context.Context
	opts StreamOptions
	out  chan<- LANEvent

	states map[string]*lanDeviceState
	cache  *discoverycache.Cache // nil when unavailable or not requested
	dirty  bool

	emissions chan MDNSService
	results   chan lanProbeResult
	retries   chan string
	sem       chan struct{}
	wg        sync.WaitGroup
	timers    []*time.Timer

	graceElapsed bool

	// pendingProbes tracks the cached identities whose initial probe has not
	// concluded yet; probesDone closes when the set empties.
	pendingProbes    map[string]bool
	probesDone       chan struct{}
	probesDoneClosed bool
}

// runLANStream drives one session. probesDone (may be nil) is closed once
// every cached entry's initial probe has concluded — Task 4's settle gate.
func runLANStream(ctx context.Context, opts StreamOptions, out chan<- LANEvent, probesDone chan struct{}) {
	s := &lanStream{
		ctx:           ctx,
		opts:          opts,
		out:           out,
		states:        make(map[string]*lanDeviceState),
		emissions:     make(chan MDNSService),
		results:       make(chan lanProbeResult),
		retries:       make(chan string, 1),
		sem:           make(chan struct{}, probeWorkers),
		pendingProbes: make(map[string]bool),
		probesDone:    probesDone,
	}
	defer s.finish()

	if opts.UseCache {
		s.emitCached()
	}
	s.closeProbesDoneIfIdle()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runBackend()
	}()

	grace := time.NewTimer(offlineGrace)
	defer grace.Stop()

	// The flush ticker exists only when the session persists what it learns.
	var flushC <-chan time.Time
	if opts.UseCache {
		flush := time.NewTicker(cacheFlushDelay)
		defer flush.Stop()
		flushC = flush.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case svc := <-s.emissions:
			s.handleSighting(svc)
		case res := <-s.results:
			s.handleProbeResult(res)
		case <-grace.C:
			s.handleGrace()
		case key := <-s.retries:
			s.handleRetry(key)
		case <-flushC:
			s.flush()
		}
	}
}

// finish tears the session down: pending retry timers are stopped, in-flight
// probes are awaited (they all abort on ctx, and waiting keeps the seam vars
// stable for the next session), and anything learned is persisted once.
func (s *lanStream) finish() {
	for _, t := range s.timers {
		t.Stop()
	}
	s.wg.Wait()
	s.flush()
	s.pendingProbes = nil
	s.closeProbesDoneIfIdle()
}

// emitCached loads the device cache and replays every fresh entry, scheduling
// each one's verification probe. A cache that cannot be loaded is treated as
// an empty one: the cache is an accelerator and must never fail a scan.
func (s *lanStream) emitCached() {
	cache, err := cacheLoadFn()
	if err != nil || cache == nil {
		return
	}
	s.cache = cache

	now := time.Now()
	for _, entry := range cache.Fresh(now) {
		key := discoverycache.Key(entry.ID, entry.DisplayName)
		if _, dup := s.states[key]; dup {
			continue
		}
		st := &lanDeviceState{dev: entry.Device(), fromCache: true}
		s.states[key] = st
		s.emit(LANEvent{Kind: LANCached, Device: st.dev})
		if s.opts.Prober != nil {
			s.pendingProbes[key] = true
			s.scheduleProbe(key, st)
		}
	}
}

// runBackend keeps the platform mDNS stream alive for the session. A non-nil
// error while ctx is still live (mDNSResponder restart, D-Bus disconnect) is
// retried with a fixed delay; after the last attempt the session keeps
// serving probe and timer events with whatever it already knows.
func (s *lanStream) runBackend() {
	emit := func(svc MDNSService) {
		select {
		case s.emissions <- svc:
		case <-s.ctx.Done():
		}
	}
	for attempt := 0; ; attempt++ {
		err := lanBackendFn(s.ctx, wendyServiceType, emit)
		if err == nil || s.ctx.Err() != nil {
			return
		}
		if attempt >= backendRetries {
			log.Printf("discovery: LAN stream backend stopped: %v", err)
			return
		}
		select {
		case <-time.After(backendRetryDelay):
		case <-s.ctx.Done():
			return
		}
	}
}

// handleSighting folds one live mDNS answer into the session.
func (s *lanStream) handleSighting(svc MDNSService) {
	dev := lanDeviceFromService(svc)
	key := discoverycache.Key(dev.ID, dev.DisplayName)
	now := time.Now()

	st, known := s.states[key]
	if !known {
		st = &lanDeviceState{dev: dev, confirmed: true}
		s.states[key] = st
		s.persist(st.dev, now)
		s.emit(LANEvent{Kind: LANFound, Device: st.dev})
		s.scheduleProbe(key, st)
		return
	}

	updated := applySighting(st.dev, dev)
	targetMoved := probeTargetChanged(st.dev, updated)
	changed := mdnsFieldsChanged(st.dev, updated)
	st.dev = updated
	if targetMoved {
		// Nothing has verified this address yet, so the row must stop claiming
		// probe-confirmed data until the retargeted probe below answers.
		st.probeConfirmed = false
	}
	s.persist(updated, now)

	switch {
	case !st.confirmed:
		// A cached (or offlined) row just proved it is on the network.
		st.confirmed = true
		st.offline = false
		s.emit(LANEvent{Kind: LANFound, Device: updated, Probed: st.probeConfirmed})
	case changed:
		s.emit(LANEvent{Kind: LANUpdated, Device: updated, Probed: st.probeConfirmed})
	}

	// Retarget: a probe aimed at the old address can no longer speak for this
	// device — whether it is still in flight (its result is discarded by
	// generation), failed there, or succeeded there.
	if targetMoved {
		s.scheduleProbe(key, st)
	}
}

// handleProbeResult folds one prober outcome into the session.
func (s *lanStream) handleProbeResult(res lanProbeResult) {
	st, known := s.states[res.key]
	if !known || res.gen != st.probeGen {
		// Stale: the probe target moved and a fresh probe is already running.
		return
	}
	st.probing = false
	defer s.probeConcluded(res.key)

	if res.err != nil {
		st.probeFailed = true
		if st.fromCache && !st.confirmed && s.graceElapsed {
			s.markOffline(res.key, st)
		}
		return
	}

	st.probeFailed = false
	st.probeConfirmed = true
	st.offline = false
	st.dev = applyProbe(st.dev, res.dev)
	s.persist(st.dev, time.Now())

	kind := LANUpdated
	if !st.confirmed {
		kind = LANFound
		st.confirmed = true
	}
	s.emit(LANEvent{Kind: kind, Device: st.dev, Probed: true})
}

// handleGrace runs once, offlineGrace after session start: every cached row
// that neither answered its probe nor showed up on the network is offline.
func (s *lanStream) handleGrace() {
	s.graceElapsed = true
	for key, st := range s.states {
		if st.fromCache && !st.confirmed && st.probeFailed {
			s.markOffline(key, st)
		}
	}
}

// markOffline emits the offline marker for a cached row and arms its single
// re-probe. The row stays listed and selectable.
func (s *lanStream) markOffline(key string, st *lanDeviceState) {
	if st.offline {
		return
	}
	st.offline = true
	s.emit(LANEvent{Kind: LANOffline, Device: st.dev})

	if st.retryScheduled || s.opts.Prober == nil {
		return
	}
	st.retryScheduled = true
	s.timers = append(s.timers, time.AfterFunc(offlineRetryDelay, func() {
		select {
		case s.retries <- key:
		case <-s.ctx.Done():
		}
	}))
}

// handleRetry runs the one post-offline re-probe (devices mid-boot).
func (s *lanStream) handleRetry(key string) {
	st, known := s.states[key]
	if !known || st.confirmed || st.probing {
		return
	}
	s.scheduleProbe(key, st)
}

// scheduleProbe verifies a device against its current address on the probe
// pool. The result is delivered to the engine loop, never applied here.
func (s *lanStream) scheduleProbe(key string, st *lanDeviceState) {
	prober := s.opts.Prober
	if prober == nil {
		return
	}
	st.probeGen++
	st.probing = true
	gen, dev := st.probeGen, st.dev

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		select {
		case s.sem <- struct{}{}:
		case <-s.ctx.Done():
			return
		}
		defer func() { <-s.sem }()

		probeCtx, cancel := context.WithTimeout(s.ctx, probeTimeout)
		defer cancel()
		probed, err := prober(probeCtx, dev)

		select {
		case s.results <- lanProbeResult{key: key, gen: gen, dev: probed, err: err}:
		case <-s.ctx.Done():
		}
	}()
}

// probeConcluded retires a cached identity's initial probe from the settle
// gate. Re-probes and probes of newly discovered devices are not tracked:
// the gate is only about the cache rows the session started with.
func (s *lanStream) probeConcluded(key string) {
	if !s.pendingProbes[key] {
		return
	}
	delete(s.pendingProbes, key)
	s.closeProbesDoneIfIdle()
}

func (s *lanStream) closeProbesDoneIfIdle() {
	if s.probesDone == nil || s.probesDoneClosed || len(s.pendingProbes) > 0 {
		return
	}
	close(s.probesDone)
	s.probesDoneClosed = true
}

// emit hands one event to the consumer, abandoning it if the session is over.
func (s *lanStream) emit(ev LANEvent) {
	select {
	case s.out <- ev:
	case <-s.ctx.Done():
	}
}

// persist records a device in the cache; the write itself is debounced by the
// flush ticker. A session without a cache keeps everything in memory.
//
// Replace, not Upsert: dev is the session's complete current view of the
// device (a live sighting's fields plus whatever a probe confirmed), so
// Upsert's non-zero-wins merge would only ever resurrect a value the device
// has since dropped — an mTLS flag or an orgid TXT record that went away.
func (s *lanStream) persist(dev models.LANDevice, now time.Time) {
	if s.cache == nil {
		return
	}
	s.cache.Replace(discoverycache.EntryFromDevice(dev), now)
	s.dirty = true
}

// flush persists what the session has learned, at most once per
// cacheFlushDelay plus once at session end.
func (s *lanStream) flush() {
	if s.cache == nil || !s.dirty {
		return
	}
	// Cleared up front: a failing write must not retry on every tick.
	s.dirty = false
	if err := s.cache.Flush(time.Now()); err != nil {
		log.Printf("discovery: persisting device cache: %v", err)
	}
}

// applySighting folds a live mDNS answer into what the session already knows.
// The answer is authoritative for everything mDNS carries — identity, address,
// and TXT-derived metadata — so a record the device stopped advertising (tls,
// orgid, name) is actually cleared rather than kept alive by a stale value.
// Only the fields no mDNS answer can speak for, the ones an agent probe fills
// in, survive from the previous view: a re-announcement must not blank a
// probed agent version until the next probe replaces it.
func applySighting(stored, sighted models.LANDevice) models.LANDevice {
	dev := sighted
	dev.AgentVersion = stored.AgentVersion
	dev.DeviceType = stored.DeviceType
	dev.OS = stored.OS
	dev.OSVersion = stored.OSVersion
	dev.CPUArchitecture = stored.CPUArchitecture
	return dev
}

// applyProbe folds an agent probe's answer into what the session already knows.
// The probe owns the version fields it read from the agent and the mTLS mode it
// actually negotiated (per the LANProber contract, that beats the tls TXT
// record); the address and mDNS metadata stay as the last sighting left them.
func applyProbe(stored, probed models.LANDevice) models.LANDevice {
	dev := stored
	dev.IsMTLS = probed.IsMTLS
	dev.AgentVersion = probed.AgentVersion
	dev.DeviceType = probed.DeviceType
	dev.OS = probed.OS
	dev.OSVersion = probed.OSVersion
	dev.CPUArchitecture = probed.CPUArchitecture
	return dev
}

// mdnsFieldsChanged reports whether anything a live sighting carries (address
// or TXT-derived metadata) differs between two views of the same device.
func mdnsFieldsChanged(old, updated models.LANDevice) bool {
	return probeTargetChanged(old, updated) ||
		old.DisplayName != updated.DisplayName ||
		old.IsMTLS != updated.IsMTLS ||
		old.AssetID != updated.AssetID ||
		old.OrgID != updated.OrgID ||
		old.MeshName != updated.MeshName ||
		old.NetworkInterface != updated.NetworkInterface
}

// probeTargetChanged reports whether a probe would now dial somewhere else.
func probeTargetChanged(old, updated models.LANDevice) bool {
	return old.IPAddress != updated.IPAddress ||
		old.Port != updated.Port ||
		old.Hostname != updated.Hostname
}
