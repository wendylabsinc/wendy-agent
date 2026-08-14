package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxDetailLen bounds the progress line shown under a running step so a long
// compiler invocation cannot wrap and break the live redraw.
const maxDetailLen = 72

// ByteProgress carries live transfer counters for a running step. It comes from
// BuildKit's own status records on the rawjson path, and from the
// "5.24MB / 27.09MB" sub-status lines on the plain-text path.
type ByteProgress struct {
	Current int64
	Total   int64   // 0 when the total is not known yet
	Rate    float64 // bytes/sec; 0 when not known
}

// Empty reports whether there is nothing worth showing.
func (b ByteProgress) Empty() bool { return b.Current <= 0 && b.Total <= 0 }

// String renders "19%  5.2MB/27.1MB  3.1MB/s", omitting the parts it lacks.
func (b ByteProgress) String() string {
	if b.Empty() {
		return ""
	}
	var sb strings.Builder
	if b.Total > 0 {
		pct := int(float64(b.Current) / float64(b.Total) * 100)
		if pct > 100 {
			pct = 100
		}
		fmt.Fprintf(&sb, "%d%%  %s/%s", pct, formatProgressBytes(b.Current), formatProgressBytes(b.Total))
	} else {
		sb.WriteString(formatProgressBytes(b.Current))
	}
	if b.Rate > 0 {
		fmt.Fprintf(&sb, "  %s/s", formatProgressBytes(int64(b.Rate)))
	}
	return sb.String()
}

// formatProgressBytes uses decimal units to match how Docker and the registries
// report layer sizes, so the numbers agree with what users see elsewhere.
func formatProgressBytes(n int64) string {
	const unit = 1000
	if n < 0 {
		return "0B"
	}
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "kMGT"[exp])
}

// Sniffers condense one line of in-container build output into the short
// progress string shown under a running step. BuildKit only ever hands us the
// raw bytes a tool printed — whether that arrives as rawjson log frames or as
// plain-text "#12 3.45 ..." lines — so both backends share these.
var (
	// pip's rich bar, which it still emits periodically without a TTY:
	// "━━━━━━ 128.0/797.3 MB 9.4 MB/s eta 0:01:11"
	sniffPipBarRe = regexp.MustCompile(`([\d.]+)/([\d.]+)\s*([kKMGT]i?B)\s+([\d.]+\s*[kKMGT]?i?B/s)`)
	// "Downloading torch-2.4.0-cp311-linux_aarch64.whl (797.3 MB)"
	sniffPipDownloadRe = regexp.MustCompile(`\b(?:Downloading|Collecting)\s+(\S+?)(?:\s+\(([\d.]+\s*[kKMGT]?i?B)\))?$`)
	// apt: "Get:5 http://deb.debian.org/debian bookworm/main arm64 libfoo 1.2-3 [1,234 kB]"
	sniffAptGetRe = regexp.MustCompile(`^Get:\d+\s+\S+\s+(.*?)\s*\[([\d,.]+\s*[kKMGT]?B)\]$`)
	// apt: "Fetched 45.6 MB in 3s (15.2 MB/s)"
	sniffAptFetchedRe = regexp.MustCompile(`^Fetched\s+([\d.]+\s*[kKMGT]?B)\s+in\s+\S+\s+\(([^)]*)\)`)
	// wget: " 50%[=====>      ] 1,234,567   1.23MB/s"
	sniffWgetRe = regexp.MustCompile(`^\s*(\d{1,3})%\[[^\]]*\]\s*([\d,]+)\s+([\d.]+\s*[kKMGT]?i?B/s)`)
	// cmake/make: "[ 42%] Building CXX object src/CMakeFiles/foo.dir/a.cpp.o"
	sniffPercentRe = regexp.MustCompile(`^\[\s*(\d{1,3})%\]\s*(.*)$`)
	// SwiftPM/ninja/cargo: "[525/1027] Compiling WendyKit Foo.swift"
	sniffCounterRe = regexp.MustCompile(`\[\s*(\d+)\s*/\s*(\d+)\s*\]\s*(.*)$`)
	// BuildKit sub-status and generic transfer lines: "5.24MB / 27.09MB"
	sniffTransferRe = regexp.MustCompile(`([\d.]+)\s*([kKMGT]?i?B)\s*/\s*([\d.]+)\s*([kKMGT]?i?B)`)

	ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
)

// sniffProgressDetail returns a compact progress string for a line of build
// output. ok is false when the line carries no recognizable progress signal —
// callers fall back to compactLogLine so the user still sees *something*.
func sniffProgressDetail(line string) (string, bool) {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" || len(line) > 1024 {
		return "", false
	}

	if m := sniffPipBarRe.FindStringSubmatch(line); m != nil {
		out := fmt.Sprintf("%s/%s %s  %s", m[1], m[2], m[3], strings.TrimSpace(m[4]))
		if cur, err1 := strconv.ParseFloat(m[1], 64); err1 == nil {
			if tot, err2 := strconv.ParseFloat(m[2], 64); err2 == nil && tot > 0 {
				out = fmt.Sprintf("%d%%  %s", int(cur/tot*100), out)
			}
		}
		return truncateDetail(out, maxDetailLen), true
	}
	if m := sniffWgetRe.FindStringSubmatch(line); m != nil {
		return truncateDetail(fmt.Sprintf("%s%%  %s  %s", m[1], m[2], strings.TrimSpace(m[3])), maxDetailLen), true
	}
	if m := sniffAptGetRe.FindStringSubmatch(line); m != nil {
		return truncateDetail(fmt.Sprintf("fetching %s [%s]", m[1], strings.TrimSpace(m[2])), maxDetailLen), true
	}
	if m := sniffAptFetchedRe.FindStringSubmatch(line); m != nil {
		return truncateDetail(fmt.Sprintf("fetched %s (%s)", strings.TrimSpace(m[1]), strings.TrimSpace(m[2])), maxDetailLen), true
	}
	if m := sniffPipDownloadRe.FindStringSubmatch(line); m != nil {
		// Keep pip's own verb: "Collecting" is dependency resolution, not a
		// download, and relabelling it would misreport what is taking time.
		verb := "downloading"
		if strings.HasPrefix(strings.TrimSpace(line), "Collecting") {
			verb = "collecting"
		}
		if m[2] != "" {
			return truncateDetail(fmt.Sprintf("%s %s (%s)", verb, m[1], strings.TrimSpace(m[2])), maxDetailLen), true
		}
		return truncateDetail(verb+" "+m[1], maxDetailLen), true
	}
	if m := sniffPercentRe.FindStringSubmatch(line); m != nil {
		return truncateDetail(strings.TrimSpace(fmt.Sprintf("%s%%  %s", m[1], m[2])), maxDetailLen), true
	}
	if m := sniffCounterRe.FindStringSubmatch(line); m != nil {
		cur, err1 := strconv.Atoi(m[1])
		tot, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil || tot <= 0 || cur > tot {
			return "", false
		}
		out := fmt.Sprintf("[%d/%d] %d%%", cur, tot, cur*100/tot)
		if rest := strings.TrimSpace(m[3]); rest != "" {
			out += "  " + rest
		}
		return truncateDetail(out, maxDetailLen), true
	}
	return "", false
}

// sniffTransferBytes pulls "5.24MB / 27.09MB" out of a BuildKit plain-progress
// sub-status line. rawjson callers use the numeric status records instead.
func sniffTransferBytes(line string) (ByteProgress, bool) {
	if len(line) > 512 {
		return ByteProgress{}, false
	}
	m := sniffTransferRe.FindStringSubmatch(line)
	if m == nil {
		return ByteProgress{}, false
	}
	cur, ok1 := parseSizeWithUnit(m[1], m[2])
	tot, ok2 := parseSizeWithUnit(m[3], m[4])
	if !ok1 || !ok2 {
		return ByteProgress{}, false
	}
	return ByteProgress{Current: cur, Total: tot}, true
}

func parseSizeWithUnit(value, unit string) (int64, bool) {
	v, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	mult, ok := byteUnitMultiplier(unit)
	if !ok {
		return 0, false
	}
	return int64(v * mult), true
}

func byteUnitMultiplier(unit string) (float64, bool) {
	switch strings.ToLower(unit) {
	case "b":
		return 1, true
	case "kb":
		return 1e3, true
	case "kib":
		return 1024, true
	case "mb":
		return 1e6, true
	case "mib":
		return 1024 * 1024, true
	case "gb":
		return 1e9, true
	case "gib":
		return 1024 * 1024 * 1024, true
	case "tb":
		return 1e12, true
	case "tib":
		return 1024 * 1024 * 1024 * 1024, true
	}
	return 0, false
}

// compactLogLine is the fallback detail: the line itself, stripped of colors and
// collapsed whitespace. A raw tail line still answers "what is it doing right
// now" for the many tools no sniffer knows about.
func compactLogLine(line string) string {
	line = stripANSI(line)
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return ""
	}
	return truncateDetail(line, maxDetailLen)
}

func stripANSI(s string) string {
	if !strings.ContainsRune(s, '\x1b') {
		return s
	}
	return ansiEscapeRe.ReplaceAllString(s, "")
}

// truncateDetail trims on rune boundaries so multi-byte output is never cut
// mid-character (which would corrupt the terminal redraw).
func truncateDetail(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	if max <= 1 {
		return string([]rune(s)[:max])
	}
	return string([]rune(s)[:max-1]) + "…"
}
