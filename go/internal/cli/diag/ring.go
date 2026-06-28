// Package diag captures structured, classified error state and a bounded
// buffer of recent log lines so an unrecoverable failure can be turned into a
// serviceable crash report. Everything here is read only when a report is
// built; collection is cheap and always on.
package diag

import "sync"

const ringCap = 200

var (
	ringMu  sync.Mutex
	ringBuf []string
)

// Record appends a line to the bounded recent-log ring. Safe for concurrent use.
func Record(line string) {
	ringMu.Lock()
	defer ringMu.Unlock()
	ringBuf = append(ringBuf, line)
	if len(ringBuf) > ringCap {
		ringBuf = ringBuf[len(ringBuf)-ringCap:]
	}
}

// Recent returns a copy of the buffered lines, oldest first.
func Recent() []string {
	ringMu.Lock()
	defer ringMu.Unlock()
	out := make([]string, len(ringBuf))
	copy(out, ringBuf)
	return out
}

// ResetForTesting clears the ring. Test-only.
func ResetForTesting() {
	ringMu.Lock()
	defer ringMu.Unlock()
	ringBuf = nil
}
