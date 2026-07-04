package timesync

import (
	"time"

	"go.uber.org/zap"
)

// Manager coordinates all time-sync sources and applies the best verified time.
// All sources call Apply; the clock is only ever advanced.
type Manager struct {
	logger     *zap.Logger
	configPath string
}

// NewManager creates a Manager. logger may be nil. configPath is the agent
// config directory (e.g. /etc/wendy-agent).
func NewManager(logger *zap.Logger, configPath string) *Manager {
	return &Manager{logger: logger, configPath: configPath}
}

// ApplyFloor reads the config-partition floor and advances the clock if it
// is ahead of the current time. Called once at agent startup.
func (m *Manager) ApplyFloor() {
	floor := readFloor(m.configPath)
	if floor.IsZero() {
		return
	}
	if err := AdvanceTo(floor, m.logger); err != nil && m.logger != nil {
		m.logger.Warn("timesync: floor apply failed", zap.Error(err))
	}
}

// Apply advances the clock to t if t is after the current time.
// Safe to call from any goroutine.
func (m *Manager) Apply(t time.Time) {
	if err := AdvanceTo(t, m.logger); err != nil && m.logger != nil {
		m.logger.Warn("timesync: apply failed", zap.Error(err))
	}
}
