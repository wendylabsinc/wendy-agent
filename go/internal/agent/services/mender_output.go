package services

import (
	"fmt"
	"strings"
	"sync"
)

// lineRing keeps the most recent maxLines non-blank lines pushed to it.
// It is safe for concurrent use because mender's stdout and stderr are
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
// ignored so the captured tail carries only meaningful mender output.
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

// menderErrorTailLines bounds how much mender output is echoed back in a
// failure message. mender's fatal cause is on the last few lines, so a small
// window keeps the gRPC error readable while still surfacing the real reason
// (e.g. an incompatible device type).
const menderErrorTailLines = 20

// formatMenderFailure builds the user-facing error for a failed mender install,
// appending the captured tail of mender's output when available. Without the
// tail this degrades to a bare "exit status N", which is what made past
// failures undiagnosable.
func formatMenderFailure(waitErr error, tail []string) string {
	if len(tail) == 0 {
		return fmt.Sprintf("mender install failed: %v", waitErr)
	}
	return fmt.Sprintf("mender install failed: %v\nmender output:\n%s", waitErr, strings.Join(tail, "\n"))
}
