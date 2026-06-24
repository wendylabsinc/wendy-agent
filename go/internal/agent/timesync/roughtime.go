package timesync

import (
	"context"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/roughtime"
	"go.uber.org/zap"
)

// backoffSchedule is the delay sequence after a failed Roughtime query.
var backoffSchedule = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	30 * time.Minute,
}

// RunDirect queries the baked-in Roughtime servers in a loop, backing off on
// failure and re-querying every 6 hours after a successful sync.
// Blocks until ctx is cancelled. Call as a goroutine.
func (m *Manager) RunDirect(ctx context.Context) {
	attempt := 0
	for {
		qCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := roughtime.Query(qCtx, Servers)
		cancel()
		if err != nil {
			if m.logger != nil {
				m.logger.Warn("timesync: direct Roughtime query failed",
					zap.Error(err), zap.Int("attempt", attempt))
			}
			delay := backoffSchedule[min(attempt, len(backoffSchedule)-1)]
			attempt++
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			continue
		}

		attempt = 0
		if m.logger != nil {
			m.logger.Info("timesync: synced via Roughtime",
				zap.String("server", result.Server),
				zap.Time("midpoint", result.Midpoint),
				zap.Duration("radius", result.Radius))
		}
		m.Apply(result.Midpoint)

		// Re-query every 6 hours.
		select {
		case <-ctx.Done():
			return
		case <-time.After(6 * time.Hour):
		}
	}
}
