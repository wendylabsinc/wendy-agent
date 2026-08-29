package commands

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Poll deadline bounds. A device that loses power mid-RPC never sends a FIN,
// so the socket black-holes: without a deadline the poll goroutine blocks until
// gRPC keepalive gives up (grpcKeepaliveTime, 15 minutes) and the refresh ticker
// stalls behind it, leaving the dashboard frozen on a dead device's last sample.
const (
	topPollTimeoutFloor = 3 * time.Second
	topPollTimeoutCap   = 15 * time.Second
)

// topPollTimeout derives the per-poll deadline from the refresh interval. Two
// intervals leaves room for a slow-but-alive device to answer, while the cap
// keeps a long --interval from also delaying the offline verdict.
func topPollTimeout(interval time.Duration) time.Duration {
	// Compare before multiplying so an extremely large duration cannot wrap
	// negative and accidentally select the floor.
	if interval <= topPollTimeoutFloor/2 {
		return topPollTimeoutFloor
	}
	if interval >= topPollTimeoutCap/2 {
		return topPollTimeoutCap
	}
	return interval * 2
}

// isDeviceUnreachable reports whether err means the device stopped answering,
// as opposed to the agent answering with a failure. Only transport-level
// outcomes count: Unavailable (refused/reset/no route) and DeadlineExceeded
// (our own poll deadline elapsing on a black-holed socket). An Internal or
// Unimplemented reply proves the device is very much alive, and Canceled is our
// own shutdown, not the device's.
func isDeviceUnreachable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, errResourceStatsUnimplemented) {
		return false
	}
	// status.FromError unwraps through fmt.Errorf("%w"), but reports ok=false
	// for a plain error — which we must not read as a transport failure.
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// formatAgeShort renders how long the device has been silent: "34s", "1m30s",
// then "1h02m" once seconds stop being the useful unit.
func formatAgeShort(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Seconds())
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%dh%02dm", secs/3600, (secs%3600)/60)
}

// topOfflineHeadline renders the offline banner text. When the last sample
// showed a pack that was draining, it names the reading — that is the
// difference between "the device is gone" and "the battery went flat". A
// charging or full pack is not evidence of anything, so it stays out: naming it
// would invent a cause.
func topOfflineHeadline(age time.Duration, b *agentpb.BatteryStats) string {
	msg := fmt.Sprintf("⚠ DEVICE OFFLINE — no response for %s", formatAgeShort(age))
	if b != nil && b.GetState() == agentpb.BatteryState_BATTERY_STATE_DISCHARGING {
		msg += fmt.Sprintf(" (last battery reading %.0f%%, discharging)", b.GetPercent())
	}
	return msg
}

// topPollTimes supplies timestamps for messages constructed directly in tests
// and guards against malformed ranges. Production pollers always provide both.
func topPollTimes(startedAt, finishedAt time.Time) (time.Time, time.Time) {
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}
	if startedAt.IsZero() || startedAt.After(finishedAt) {
		startedAt = finishedAt
	}
	return startedAt, finishedAt
}

// isOffline reports whether the newest unreachable poll began after the most
// recent reply from either polling RPC. Comparing timestamps makes the result
// independent of the order in which the two poll goroutines deliver messages.
func (m topModel) isOffline() bool {
	return !m.lastUnreachableAt.IsZero() &&
		(m.lastReplyAt.IsZero() || m.lastUnreachableAt.After(m.lastReplyAt))
}

// markOffline records the start of an outage, keeping the original timestamp
// across repeated failures so the banner's age counts up rather than resetting
// on every tick.
func (m *topModel) markOffline(startedAt time.Time) {
	wasOffline := m.isOffline()
	if m.lastUnreachableAt.IsZero() || startedAt.After(m.lastUnreachableAt) {
		m.lastUnreachableAt = startedAt
	}
	if !wasOffline && m.isOffline() {
		m.offlineSince = startedAt
	}
	// Before the first reply, failures can still arrive out of order. Preserve
	// the earliest failed poll start so the displayed silence does not reset.
	if m.lastReplyAt.IsZero() && m.isOffline() &&
		(m.offlineSince.IsZero() || startedAt.Before(m.offlineSince)) {
		m.offlineSince = startedAt
	}
}

// silentFor returns how long the device has been unresponsive, measured from
// the last reply from either poll — or from the start of the outage when no
// poll ever succeeded, since "silent since the dawn of time" is not useful.
func (m topModel) silentFor(now time.Time) time.Duration {
	base := m.lastReplyAt
	if base.IsZero() {
		base = m.offlineSince
	}
	if base.IsZero() {
		return 0
	}
	return now.Sub(base)
}

// noteReachable records proof that the agent answered. A stale reply cannot
// clear a newer failed poll, while a reply newer than every failed poll ends the
// outage. The stats sample carries its own timestamp separately.
func (m *topModel) noteReachable(at time.Time) {
	if m.lastReplyAt.IsZero() || at.After(m.lastReplyAt) {
		m.lastReplyAt = at
	}
	if !m.isOffline() {
		m.offlineSince = time.Time{}
	}
}

// noteOfflineErr folds a failed poll into the model: a transport failure raises
// the offline banner (which supersedes the flash line — the banner already says
// what went wrong), anything else stays a flash.
//
// A non-transport error also clears an outage. The agent answering at all —
// even to say the sampler blew up — is proof the device came back, and leaving
// "no response for 41s" on screen beside a fresh reply would be a lie. The
// meters stay frozen either way, since there is still no new sample.
func (m *topModel) noteOfflineErr(err error, startedAt, finishedAt time.Time) {
	if isDeviceUnreachable(err) {
		m.markOffline(startedAt)
		m.flash = ""
		return
	}
	m.noteReachable(finishedAt)
	m.flash = userFacingGRPCError(err)
}

// noteOnline records a successful stats poll: the device is reachable and the
// readings are as of now.
func (m *topModel) noteOnline(at time.Time) {
	m.noteReachable(at)
}
