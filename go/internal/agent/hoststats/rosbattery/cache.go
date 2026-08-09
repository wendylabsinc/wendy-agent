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
// use: the monitor goroutine calls Put while gRPC handlers call Battery.
type Cache struct {
	now func() time.Time

	mu   sync.RWMutex
	b    *hoststats.Battery
	seen time.Time
}

// NewCache returns an empty cache reading time from now.
func NewCache(now func() time.Time) *Cache {
	return &Cache{now: now}
}

// Put stores a sample and restarts its staleness window. A nil sample clears
// the cache, which is how the monitor reports that its writer went away. The
// sample is copied, so the caller may reuse its buffer.
func (c *Cache) Put(b *hoststats.Battery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b == nil {
		c.b, c.seen = nil, time.Time{}
		return
	}
	cp := *b
	c.b, c.seen = &cp, c.now()
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
