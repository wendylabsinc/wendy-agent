package flasher

import "time"

// stallTimeout is how long Run tolerates neither push bytes nor flash-log
// output changing before it declares bootburn wedged and kills it. Partition
// writes are chunked (each 10 MiB chunk is a push, so progress keeps moving);
// the only monolithic silent windows are single device-side ops with the peer
// shim lock-blocked (a multi-GiB blkdiscard, md5 verify, or the end-of-flash
// resize2fs — single-digit minutes on NVMe, measured within a ~23-minute total
// flash). The costs are asymmetric: a genuine deadlock is permanent so a later
// kill only delays the diagnosis, while a false kill interrupts partition
// writes and can leave the device unbootable — so err well above the longest
// legitimate window.
const stallTimeout = 15 * time.Minute

// stallDetector detects a wedged bootburn: neither the shim's push byte
// counter nor the flash log has changed for a full window. Both signals go
// quiet together only when no host-side flash work is happening at all.
type stallDetector struct {
	window   time.Duration
	last     time.Time // last time either signal changed
	prevPush int64
	prevLog  int64
}

func newStallDetector(window time.Duration, now time.Time) *stallDetector {
	return &stallDetector{window: window, last: now}
}

// observe feeds the current cumulative push byte count and flash-log size,
// returning true when neither has changed for the full window. Any change —
// including a counter reset — counts as progress.
func (s *stallDetector) observe(now time.Time, pushBytes, logBytes int64) bool {
	if pushBytes != s.prevPush || logBytes != s.prevLog {
		s.prevPush, s.prevLog = pushBytes, logBytes
		s.last = now
		return false
	}
	return now.Sub(s.last) >= s.window
}
