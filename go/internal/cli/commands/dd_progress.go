//go:build darwin || linux

package commands

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// maxDDDiagnostics caps the diagnostic output captured from dd's stderr so a
// runaway or hostile subprocess cannot grow the error message without bound.
const maxDDDiagnostics = 4 << 10 // 4 KiB

// scanDDProgress parses dd's `status=progress` output, invoking progressFn with
// the running byte count, and returns dd's non-progress diagnostic output
// (e.g. error messages and the "records in/out" summary) for inclusion in an
// error message. Progress updates are never retained, so the returned string
// stays small even for multi-minute writes; the diagnostic capture is also
// sanitized of control characters and bounded to maxDDDiagnostics bytes so dd's
// stderr cannot inject terminal escape sequences or unbounded spam into our
// error output. progressFn may be nil (direct-install mode), in which case the
// byte counts are parsed and discarded but diagnostics are still captured.
//
// Both Linux GNU dd and macOS BSD dd (Monterey+) emit lines whose first
// non-whitespace token is the byte count, e.g.:
//
//	524288000 bytes (524 MB, 500 MiB) copied, 1 s, 524 MB/s        (Linux)
//	   71491911680 bytes (71 GB, 67 GiB) transferred 1.003s, ...   (macOS, padded)
//	209715200 bytes transferred in 2.500 secs (...)                (macOS final)
//
// macOS dd right-pads the number with spaces so the columns stay aligned as
// digits grow, so we trim leading whitespace before tokenizing.
//
// "records in" / "records out" lines have a "+0" suffix on the first token
// so ParseInt rejects them; they are captured as diagnostics rather than
// treated as progress.
func scanDDProgress(r io.Reader, progressFn func(written int64)) string {
	var diag strings.Builder
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	scanner.Split(splitCROrLF)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) > 0 {
			if written, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				if progressFn != nil {
					progressFn(written)
				}
				continue
			}
		}
		appendDDDiagnostic(&diag, line)
	}
	return strings.TrimSpace(diag.String())
}

// appendDDDiagnostic appends a single non-progress line from dd's stderr to b,
// stripping control characters and respecting the maxDDDiagnostics cap.
func appendDDDiagnostic(b *strings.Builder, line string) {
	if b.Len() >= maxDDDiagnostics {
		return
	}
	cleaned := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\t' || (r >= 0x20 && r != 0x7f) {
			return r
		}
		return -1
	}, line))
	if cleaned == "" {
		return
	}
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	if room := maxDDDiagnostics - b.Len(); len(cleaned) > room {
		cleaned = cleaned[:room]
	}
	b.WriteString(cleaned)
}

// splitCROrLF is a bufio.SplitFunc that splits on '\r' or '\n'.
func splitCROrLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
