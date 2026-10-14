package timesync

import (
	"context"
	"fmt"
	"math"
	"time"
)

// TimeWindow bounds current time using a fresh authenticated consensus. A
// time-gated operation must be valid throughout it, not just at its midpoint.
type TimeWindow struct {
	Earliest time.Time
	Latest   time.Time
}

// FreshTime obtains new nonce-bound signed responses from the existing baked-in
// servers. It neither reads/writes clock_floor nor disciplines the system clock:
// boot continuity and persistence remain the existing time services' job.
func (m *Manager) FreshTime(ctx context.Context) (TimeWindow, error) {
	queryCtx, cancel := context.WithTimeout(ctx, maxCaptureRTT)
	defer cancel()
	c, err := m.queryConsensus(queryCtx, Servers)
	if err != nil {
		return TimeWindow{}, fmt.Errorf("timesync: fresh authenticated query failed: %w", err)
	}
	if err := queryCtx.Err(); err != nil {
		return TimeWindow{}, err
	}
	if !disciplinesClock(c) || c.Quorum < 2 || c.UpperOffsetNanos < c.LowerOffsetNanos {
		return TimeWindow{}, fmt.Errorf("timesync: fresh authenticated quorum required")
	}
	boot, err := bootTimeNanos()
	if err != nil {
		return TimeWindow{}, err
	}
	if boot < 0 || c.UpperOffsetNanos > math.MaxInt64-boot {
		return TimeWindow{}, fmt.Errorf("timesync: authenticated upper bound overflow")
	}
	window := TimeWindow{
		Earliest: time.Unix(0, boot+c.LowerOffsetNanos),
		Latest:   time.Unix(0, boot+c.UpperOffsetNanos),
	}
	if window.Earliest.Before(floorMin) || window.Latest.After(floorMax) {
		return TimeWindow{}, fmt.Errorf("timesync: implausible authenticated interval")
	}
	return window, nil
}
