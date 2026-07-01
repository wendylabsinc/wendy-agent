package services

import (
	"regexp"
	"strconv"
	"strings"
)

// csiRemnantPattern strips orphaned ANSI/VT escape remnants left after ESC
// (U+001B, removed by strings.Map's r < 0x20 check) is stripped:
//   - Standard CSI: "[" + decimal/semicolon params + letter (e.g. "[31m")
//   - Private/intermediate CSI: "[" + "?", "!", "<", ">", "=" + params + letter
//     (e.g. "[?25l" cursor visibility, "[>c" device attributes)
//   - OSC remnants: "]" + numeric param + ";" + text, terminator already removed
//     by the control-char strip above (BEL=0x07, ST=0x9c are both <0x20 or C1)
var csiRemnantPattern = regexp.MustCompile(`\[[0-9;?!<>=]*[A-Za-z]|\]\d+;[^\x00-\x1f]*`)

// parseKmsgLine parses a /dev/kmsg record of the form:
//
//	priority,sequence,timestamp_us,-;message
//
// Returns the syslog level (0–7), sanitised message text, timestamp in
// microseconds since boot, and whether parsing succeeded. ASCII and Unicode
// control characters (except tab) are stripped to prevent log injection.
//
// This parsing is pure (no syscalls) and lives outside the linux build tag so
// both the OTel streaming collector (CollectDmesgLogs) and the one-shot
// inspection dump (DumpKernelLog) can share it and be tested on any platform.
func parseKmsgLine(line string) (level int, message string, timestampUS int64, ok bool) {
	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return 0, "", 0, false
	}

	// Strip ASCII control chars and Unicode format/control characters.
	message = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		// Drop ASCII control chars (C0/C1) and selected Unicode characters that
		// could be used for log injection or terminal escape sequences:
		//   0x200B zero-width space, 0x200E/0x200F directional marks, 0xFEFF BOM
		//   0x2028–0x2029 line/paragraph separators
		//   0x202A–0x202E bidirectional override characters (LRE/RLE/PDF/LRO/RLO)
		//   0x2066–0x2069 bidirectional isolation characters (LRI/RLI/FSI/PDI)
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) ||
			r == 0x200b || r == 0x200e || r == 0x200f || r == 0xfeff ||
			(r >= 0x2028 && r <= 0x202e) ||
			(r >= 0x2066 && r <= 0x2069) {
			return -1
		}
		return r
	}, line[semi+1:])
	// Strip orphaned CSI remnants (e.g. "[31m") left after ESC is removed above.
	message = csiRemnantPattern.ReplaceAllString(message, "")

	parts := strings.SplitN(line[:semi], ",", 4)
	if len(parts) < 3 {
		return 0, "", 0, false
	}

	priority, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", 0, false
	}
	// The kmsg priority byte is facility|level (8 bits). Reject values outside
	// this range — a negative or oversized value indicates a crafted/malformed
	// record and could silently coerce to an unexpected severity via & 7.
	if priority < 0 || priority > 0xFF {
		return 0, "", 0, false
	}

	ts, err := strconv.ParseInt(parts[2], 10, 64)
	// Reject parse failures and negative timestamps. A failed parse leaves ts=0,
	// which is a valid boot-epoch timestamp, so we must distinguish the two cases
	// explicitly rather than relying on kmsgTimestampToWall's range guard.
	if err != nil || ts < 0 {
		return 0, "", 0, false
	}
	return priority & 7, message, ts, true
}
