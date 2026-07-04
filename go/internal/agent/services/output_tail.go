package services

import (
	"strings"
	"sync"
)

// lineRing keeps the most recent maxLines non-blank lines pushed to it. It is
// safe for concurrent use because an update backend's stdout and stderr are
// scanned from separate goroutines.
type lineRing struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
}

func newLineRing(maxLines int) *lineRing {
	if maxLines < 1 {
		maxLines = 1
	}
	return &lineRing{maxLines: maxLines}
}

// push records a line, dropping the oldest when at capacity. Blank lines are
// ignored so the captured tail carries only meaningful update-backend output.
func (r *lineRing) push(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
	if len(r.lines) > r.maxLines {
		r.lines = r.lines[len(r.lines)-r.maxLines:]
	}
}

// tail returns a copy of the retained lines, oldest first.
func (r *lineRing) tail() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

// updaterOutputTailLines bounds how much update-backend output is retained for
// a failure message. The fatal cause is typically on the last few lines, so a
// small window keeps a gRPC error readable while still surfacing the real
// reason (e.g. an incompatible device type).
const updaterOutputTailLines = 20
