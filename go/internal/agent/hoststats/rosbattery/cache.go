package rosbattery

import (
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/agent/hoststats"
)

// StaleAfter is how long a sample stays usable. BatteryState republishers
// typically run at 1-10 Hz and LowState far faster, so this is generous for
// both while making a dead publisher vanish from `wendy device top` within a
// refresh or two. It is deliberately tight: the reading's source is not
// exposed anywhere, so staleness is the only thing preventing a stale number
// from rendering as a confident one.
const StaleAfter = 15 * time.Second

// Cache holds the newest decoded sample and expires it. Safe for concurrent
// use: the monitor goroutine writes while gRPC handlers read battery and
// temperature data.
type Cache struct {
	now func() time.Time

	mu    sync.RWMutex
	b     *hoststats.Battery
	zones []hoststats.ThermalZone
	seen  time.Time
}

// NewCache returns an empty cache reading time from now.
func NewCache(now func() time.Time) *Cache {
	return &Cache{now: now}
}

// Put stores a sample and restarts its staleness window. A nil sample clears
// the cache, which is how the monitor reports that its writer went away. The
// sample is copied, so the caller may reuse its buffer.
func (c *Cache) Put(b *hoststats.Battery) {
	c.putTelemetry(b, nil)
}

func (c *Cache) putTelemetry(b *hoststats.Battery, zones []hoststats.ThermalZone) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b == nil {
		c.b, c.zones, c.seen = nil, nil, time.Time{}
		return
	}
	cp := *b
	c.b, c.zones, c.seen = &cp, append([]hoststats.ThermalZone(nil), zones...), c.now()
}

// ThermalZones returns copies of the newest device-specific temperatures, or
// nil when the cache is empty or stale. They share the battery sample's
// staleness window because both values come from the same LowState message.
func (c *Cache) ThermalZones() []hoststats.ThermalZone {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.b == nil || c.now().Sub(c.seen) > StaleAfter {
		return nil
	}
	return append([]hoststats.ThermalZone(nil), c.zones...)
}

// Battery returns a copy of the newest sample, or nil when the cache is empty
// or the sample has gone stale.
func (c *Cache) Battery() *hoststats.Battery {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.b == nil || c.now().Sub(c.seen) > StaleAfter {
		return nil
	}
	cp := *c.b
	return &cp
}
