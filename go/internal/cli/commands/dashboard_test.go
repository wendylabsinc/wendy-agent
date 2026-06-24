package commands

import (
	"regexp"
	"strings"
	"testing"

	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// ansiRE strips SGR color codes (and any other escape sequences) so tests can
// assert on the visible content of a rendered row.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func strBody(s string) *otelpb.AnyValue {
	return &otelpb.AnyValue{Value: &otelpb.AnyValue_StringValue{StringValue: s}}
}

func strAttr(key, val string) *otelpb.KeyValue {
	return &otelpb.KeyValue{Key: key, Value: strBody(val)}
}

func TestSanitizeLogText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"carriage return dropped", "abc\rdef", "abcdef"},
		{"cursor erase escape removed", "pulling manifest ⠋\x1b[2K", "pulling manifest ⠋"},
		{"cursor up escape removed", "x\x1b[1Ay", "xy"},
		{"sgr color removed", "\x1b[31mred\x1b[0m", "red"},
		{"tab becomes space", "a\tb", "a b"},
		{"nul and bell dropped", "a\x00\x07b", "ab"},
		{"newline preserved", "line1\nline2", "line1\nline2"},
		{"spinner braille preserved", "⠋⠙⠹", "⠋⠙⠹"},
		{"osc title with bel terminator dropped", "\x1b]0;evil title\x07after", "after"},
		{"osc title with st terminator dropped", "\x1b]0;evil title\x1b\\after", "after"},
		{"dcs sequence dropped", "\x1bPq#payload\x1b\\done", "done"},
		{"8-bit csi dropped", "before\x9b2Kafter", "beforeafter"},
		{"two-char escape does not swallow following text", "x\x1b7y", "xy"},
		{"two-char keypad escape dropped", "a\x1b=b", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeLogText(tc.in); got != tc.want {
				t.Fatalf("sanitizeLogText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// noRowContainsControlBytes asserts the layout-breaking bytes never survive into
// a rendered row. Raw newline/carriage-return and cursor-movement/erase escapes
// would corrupt the BubbleTea stdout grid.
func noRowContainsControlBytes(t *testing.T, rows []string) {
	t.Helper()
	for i, row := range rows {
		if strings.ContainsAny(row, "\n\r") {
			t.Fatalf("row %d contains raw newline/carriage return: %q", i, row)
		}
		// After stripping SGR color codes, no escape byte should remain.
		if strings.ContainsRune(stripANSI(row), '\x1b') {
			t.Fatalf("row %d contains a non-color escape sequence: %q", i, row)
		}
	}
}

func TestFormatLogLinesMultilineSplitsIntoRows(t *testing.T) {
	lr := &otelpb.LogRecord{Body: strBody("line1\nline2\nline3")}
	rows := formatLogLines("svc", lr)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d: %#v", len(rows), rows)
	}
	noRowContainsControlBytes(t, rows)

	if !strings.HasSuffix(stripANSI(rows[0]), "line1") {
		t.Errorf("row 0 should end with body line1, got %q", stripANSI(rows[0]))
	}
	// Continuation rows are indented under the prefix and carry only body text.
	for i, want := range []string{"line2", "line3"} {
		row := stripANSI(rows[i+1])
		if !strings.HasPrefix(row, " ") {
			t.Errorf("continuation row %d should be indented, got %q", i+1, row)
		}
		if strings.TrimLeft(row, " ") != want {
			t.Errorf("continuation row %d = %q, want indented %q", i+1, row, want)
		}
	}
}

func TestFormatLogLinesStripsProgressControlSequences(t *testing.T) {
	// A spinner line as produced by `ollama pull`: trailing erase + carriage return.
	lr := &otelpb.LogRecord{Body: strBody("pulling manifest ⠋\x1b[2K\r")}
	rows := formatLogLines("llm-app", lr)

	if len(rows) != 1 {
		t.Fatalf("expected 1 row (trailing empty dropped), got %d: %#v", len(rows), rows)
	}
	noRowContainsControlBytes(t, rows)
	if !strings.Contains(stripANSI(rows[0]), "pulling manifest ⠋") {
		t.Errorf("row should retain visible body, got %q", stripANSI(rows[0]))
	}
}

func TestFormatLogLinesAttributesOnLastRow(t *testing.T) {
	lr := &otelpb.LogRecord{
		Body:       strBody("a\nb"),
		Attributes: []*otelpb.KeyValue{strAttr("stream", "stderr")},
	}
	rows := formatLogLines("svc", lr)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %#v", len(rows), rows)
	}
	noRowContainsControlBytes(t, rows)
	last := stripANSI(rows[len(rows)-1])
	if !strings.Contains(last, "stream=stderr") {
		t.Errorf("attributes should be appended to the last row, got %q", last)
	}
	if strings.Contains(stripANSI(rows[0]), "stream=stderr") {
		t.Errorf("attributes should not appear on the first row, got %q", stripANSI(rows[0]))
	}
}

func TestFormatLogLinesPreservesInteriorBlankLines(t *testing.T) {
	// A body with an intentional interior blank line and a single terminating
	// newline: the interior blank is kept, only the terminator empty is dropped.
	lr := &otelpb.LogRecord{Body: strBody("a\n\nb\n")}
	rows := formatLogLines("svc", lr)

	if len(rows) != 3 {
		t.Fatalf("expected 3 rows (interior blank kept, terminator dropped), got %d: %#v", len(rows), rows)
	}
	noRowContainsControlBytes(t, rows)
	if got := strings.TrimSpace(stripANSI(rows[1])); got != "" {
		t.Errorf("row 1 should be the preserved blank line, got %q", got)
	}
	if !strings.HasSuffix(stripANSI(rows[2]), "b") {
		t.Errorf("row 2 should carry body line b, got %q", stripANSI(rows[2]))
	}
}

func TestFormatLogLinesNilBody(t *testing.T) {
	lr := &otelpb.LogRecord{Attributes: []*otelpb.KeyValue{strAttr("k", "v")}}
	rows := formatLogLines("svc", lr)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row for body-less record, got %d: %#v", len(rows), rows)
	}
	noRowContainsControlBytes(t, rows)
	if !strings.Contains(stripANSI(rows[0]), "k=v") {
		t.Errorf("expected attributes on the single row, got %q", stripANSI(rows[0]))
	}
}
