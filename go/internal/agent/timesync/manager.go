package timesync

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
	"go.uber.org/zap"
)

// floorRefreshInterval is how often RunFloorRefresh persists the verified time.
// A device that reboots without network then starts at most this far behind
// the last moment the agent knew the real time.
const floorRefreshInterval = time.Hour

// Manager coordinates all time-sync sources and applies the best verified time.
// All sources call Apply; the clock is only ever advanced.
type Manager struct {
	logger     *zap.Logger
	configPath string
	mu         sync.RWMutex
	latest     *Consensus

	// verifiedOffset is this boot's latest verified time as an offset from the
	// boot clock (wall time minus CLOCK_BOOTTIME), valid once haveVerified is
	// set. The boot clock plus this offset is a verified time that keeps
	// advancing, whatever later happens to the wall clock. Guarded by mu.
	verifiedOffset int64
	haveVerified   bool

	// floorMu serializes the read-compare-write of the verified clock floor
	// across the sync loops, the SyncClock RPC and the periodic refresh.
	floorMu sync.Mutex

	// Injection points for tests of time-sync loops. Nil means use the real
	// network, clock write, retry timer and boot clock, so a zero-value Manager
	// still behaves.
	query    func(context.Context, []roughtime.Server) (Consensus, error)
	apply    func(time.Time)
	sleep    func(time.Duration) <-chan time.Time
	bootTime func() (int64, error)

	multicastListen     func() (multicastPacketConn, error)
	multicastInterfaces func() ([]net.Interface, error)
	multicastInterval   time.Duration
}

func (m *Manager) queryConsensus(ctx context.Context, servers []roughtime.Server) (Consensus, error) {
	if m.query != nil {
		return m.query(ctx, servers)
	}
	return QueryConsensus(ctx, servers)
}

func (m *Manager) applyTime(t time.Time) {
	if m.apply != nil {
		m.apply(t)
		return
	}
	m.Apply(t)
}

func (m *Manager) after(d time.Duration) <-chan time.Time {
	if m.sleep != nil {
		return m.sleep(d)
	}
	return time.After(d)
}

func (m *Manager) bootNanos() (int64, error) {
	if m.bootTime != nil {
		return m.bootTime()
	}
	return bootTimeNanos()
}

func (m *Manager) RecordConsensus(c Consensus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := c
	m.latest = &copy
}
func (m *Manager) LatestConsensus() (Consensus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.latest == nil {
		return Consensus{}, false
	}
	return *m.latest, true
}

// NewManager creates a Manager. logger may be nil. configPath is the agent
// config directory (e.g. /etc/wendy-agent).
func NewManager(logger *zap.Logger, configPath string) *Manager {
	return &Manager{logger: logger, configPath: configPath}
}

// ApplyFloor advances the clock to the later of the two persisted floors when
// that is ahead of the current time: the install-time floor from the config
// partition, and the verified floor this agent records while it runs. Called
// once at agent startup.
func (m *Manager) ApplyFloor() {
	floor := m.persistedFloor()
	if floor.IsZero() {
		return
	}
	if err := AdvanceTo(floor, m.logger); err != nil && m.logger != nil {
		m.logger.Warn("timesync: floor apply failed", zap.Error(err))
	}
}

// persistedFloor returns the later of the install-time and verified floors, or
// zero when neither holds a plausible value. Implausible values are logged and
// ignored.
func (m *Manager) persistedFloor() time.Time {
	floor, refused := readFloor(m.configPath)
	if !refused.IsZero() && m.logger != nil {
		m.logger.Warn("timesync: ignoring implausible clock floor",
			zap.Time("floor", refused),
			zap.Time("min", floorMin), zap.Time("max", floorMax))
	}
	verified, refused := readVerifiedFloor(m.configPath)
	if !refused.IsZero() && m.logger != nil {
		m.logger.Warn("timesync: ignoring implausible verified clock floor",
			zap.Time("floor", refused),
			zap.Time("min", floorMin), zap.Time("max", floorMax))
	}
	if verified.After(floor) {
		return verified
	}
	return floor
}

// ApplyCertificateFloor advances the clock to notBefore, the device
// certificate's NotBefore. The cloud issued that certificate, so real time is
// at least notBefore. Without this, a device that lost power and network boots
// from its RTC or clock floor, which can be weeks older, and then rejects every
// client certificate issued since (WDY-3204). A zero or implausible notBefore
// is ignored.
func (m *Manager) ApplyCertificateFloor(notBefore time.Time) {
	if notBefore.Before(floorMin) || notBefore.After(floorMax) {
		return
	}
	if now := time.Now(); now.Before(notBefore) && m.logger != nil {
		m.logger.Warn("timesync: clock is behind the device certificate; advancing to its NotBefore",
			zap.Time("clock", now), zap.Time("notBefore", notBefore))
	}
	m.applyTime(notBefore)
}

// Apply advances the clock to t if t is after the current time.
// Safe to call from any goroutine.
func (m *Manager) Apply(t time.Time) {
	if err := AdvanceTo(t, m.logger); err != nil && m.logger != nil {
		m.logger.Warn("timesync: apply failed", zap.Error(err))
	}
}

// ApplyVerified advances the clock to t, a time read from a verified Roughtime
// proof, then records t as this boot's verified time and persists it as the
// verified clock floor. The clock is set first: persisting is best effort and
// never decides whether a verified time is applied.
// Safe to call from any goroutine.
func (m *Manager) ApplyVerified(t time.Time) {
	m.applyTime(t)
	boot, err := m.bootNanos()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("timesync: cannot read the boot clock; verified clock floor not updated", zap.Error(err))
		}
		return
	}
	m.noteRelayedProof(t.UnixNano() - boot)
}

// noteRelayedProof records offset (a relayed proof's time minus the boot clock)
// as this boot's verified time if it is the latest seen, and persists the time
// it implies. A relayed proof answers no nonce of ours, so an old proof
// replayed on the LAN still verifies; keeping the latest stops it pulling the
// verified time back.
func (m *Manager) noteRelayedProof(offset int64) {
	m.mu.Lock()
	if !m.haveVerified || offset > m.verifiedOffset {
		m.verifiedOffset = offset
		m.haveVerified = true
	}
	m.mu.Unlock()
	m.persistVerifiedFloor(time.Time{})
}

// noteDirectConsensus records the lower bound of a direct Roughtime consensus
// as this boot's verified time and persists it. A direct result answers a
// nonce this device chose, so it cannot be a replay. It replaces the verified
// time outright, and its upper bound caps the persisted floor. That is how a
// floor pushed ahead by a bad relayed proof, or by a fast oscillator between
// syncs, comes back down.
func (m *Manager) noteDirectConsensus(lowerOffset, upperOffset int64) {
	m.mu.Lock()
	m.verifiedOffset = lowerOffset
	m.haveVerified = true
	m.mu.Unlock()
	boot, err := m.bootNanos()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("timesync: cannot read the boot clock; verified clock floor not updated", zap.Error(err))
		}
		return
	}
	m.persistVerifiedFloor(time.Unix(0, boot+upperOffset))
}

// persistVerifiedFloor raises the verified clock floor to the boot clock plus
// this boot's verified offset. It does nothing until a verified time has been
// seen this boot, so an unverified wall clock is never persisted. When ceiling
// is set and the persisted floor is above it, the floor is lowered to the
// verified time instead.
func (m *Manager) persistVerifiedFloor(ceiling time.Time) {
	if m.configPath == "" {
		return
	}
	m.mu.RLock()
	offset, ok := m.verifiedOffset, m.haveVerified
	m.mu.RUnlock()
	if !ok {
		return
	}
	boot, err := m.bootNanos()
	if err != nil {
		if m.logger != nil {
			m.logger.Warn("timesync: cannot read the boot clock; verified clock floor not updated", zap.Error(err))
		}
		return
	}
	t := time.Unix(0, boot+offset)
	m.floorMu.Lock()
	defer m.floorMu.Unlock()
	if cur, _ := readVerifiedFloor(m.configPath); !ceiling.IsZero() && cur.After(ceiling) {
		if m.logger != nil {
			m.logger.Warn("timesync: verified clock floor is ahead of the Roughtime consensus; lowering it",
				zap.Time("floor", cur), zap.Time("consensusUpper", ceiling), zap.Time("to", t))
		}
		if err := writeVerifiedFloor(m.configPath, t); err != nil && m.logger != nil {
			m.logger.Warn("timesync: could not persist verified clock floor", zap.Error(err))
		}
		return
	}
	if _, err := advanceVerifiedFloor(m.configPath, t); err != nil && m.logger != nil {
		m.logger.Warn("timesync: could not persist verified clock floor", zap.Error(err))
	}
}

// RunFloorRefresh persists this boot's verified time as the clock floor every
// floorRefreshInterval, so the floor keeps pace with real time between syncs
// rather than staying at the last one.
// Blocks until ctx is cancelled. Call as a goroutine.
func (m *Manager) RunFloorRefresh(ctx context.Context) {
	for m.wait(ctx, floorRefreshInterval) {
		m.persistVerifiedFloor(time.Time{})
	}
}
