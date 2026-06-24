package tui

import (
	"strings"
	"testing"
)

func TestMatchRangesCaseFoldedOccurrences(t *testing.T) {
	cases := []struct {
		text, query string
		want        [][2]int
	}{
		{"my home network", "home", [][2]int{{3, 7}}},
		{"abcabcabc", "abc", [][2]int{{0, 3}, {3, 6}, {6, 9}}},
		{"aaaa", "aa", [][2]int{{0, 2}, {2, 4}}}, // non-overlapping
		{"nothing here", "zz", nil},
		{"short", "longer than text", nil},
		{"", "x", nil},
	}
	for _, c := range cases {
		got := matchRanges(lowerRunes([]rune(c.text)), lowerRunes([]rune(c.query)))
		if len(got) != len(c.want) {
			t.Errorf("matchRanges(%q, %q) = %v; want %v", c.text, c.query, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("matchRanges(%q, %q)[%d] = %v; want %v", c.text, c.query, i, got[i], c.want[i])
			}
		}
	}
}

func TestMatchRangesIsCaseInsensitive(t *testing.T) {
	got := matchRanges(lowerRunes([]rune("MyHomeNet")), lowerRunes([]rune("HOME")))
	if len(got) != 1 || got[0] != [2]int{2, 6} {
		t.Errorf("expected case-insensitive match at [2,6), got %v", got)
	}
}

func TestHighlightMatchesPreservesText(t *testing.T) {
	// Styles render as plain passthrough without a color terminal, so assert
	// the text content survives intact regardless of profile.
	view := " HomeNet    70%  \n CafeSpot   55%  "
	out := HighlightMatches(view, "home", 0, 12)
	if stripped := out; !strings.Contains(stripped, "HomeNet") || !strings.Contains(stripped, "CafeSpot") {
		t.Errorf("highlighting mangled the view: %q", out)
	}
	if got, want := len(strings.Split(out, "\n")), 2; got != want {
		t.Errorf("line count changed: got %d want %d", got, want)
	}
}

func TestHighlightMatchesSkipsANSILines(t *testing.T) {
	selected := "\x1b[7m HomeNet \x1b[0m"
	out := HighlightMatches(selected, "home", 0, 30)
	if out != selected {
		t.Errorf("ANSI line should be untouched; got %q", out)
	}
}

func TestHighlightMatchesRespectsColumnBounds(t *testing.T) {
	// "open" appears in both the SSID column (cols 0-9) and the security
	// column; only the in-bounds occurrence may be rewritten. With a plain
	// color profile the style is a no-op, so verify bounds via a query that
	// only matches outside the region: the view must come back unchanged
	// byte-for-byte (no rebuild artifacts).
	view := " WPA2Net   Open  "
	out := HighlightMatches(view, "open", 0, 8)
	if out != view {
		t.Errorf("out-of-bounds match should leave line unchanged; got %q", out)
	}
}

func TestHighlightMatchesEmptyQueryNoop(t *testing.T) {
	view := "anything"
	if out := HighlightMatches(view, "", 0, 10); out != view {
		t.Errorf("empty query must be a no-op, got %q", out)
	}
}

func TestStripControl(t *testing.T) {
	cases := []struct{ in, want string }{
		{"PlainNet", "PlainNet"},
		{"\x1b[2J\x1b[HFake Header\x1b[0m", "[2J[HFake Header[0m"},
		{"\x1b]8;;http://evil\x07click\x1b]8;;\x07", "]8;;http://evilclick]8;;"},
		{"tab\tand\nnewline", "tabandnewline"},
		{"del\x7fchar", "delchar"},
		{"c1\u009bcsi", "c1csi"}, // U+009B is a single-rune CSI introducer
		{"c1\u0085nel", "c1nel"},
		{"bidi\u202eflip", "bidiflip"}, // RLO override (Trojan Source spoofing)
		{"zero\u200bwidth\u200djoin", "zerowidthjoin"},
		{"emoji 📶 ok", "emoji 📶 ok"},
	}
	for _, c := range cases {
		if got := StripControl(c.in); got != c.want {
			t.Errorf("StripControl(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
