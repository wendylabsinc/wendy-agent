package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

// MatchHighlightStyle marks filter-matched characters: a light seafoam green
// background with black text so matches read at a glance on both dark and
// light terminals.
var MatchHighlightStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#9FE2BF")).
	Foreground(lipgloss.Color("#000000"))

// HighlightMatches re-renders a plain-text table view so every
// case-insensitive occurrence of query between rune columns start (inclusive)
// and end (exclusive) of each line is wrapped in MatchHighlightStyle. The
// column bounds restrict highlighting to the column being filtered (e.g. the
// SSID cell) so query text doesn't light up unrelated columns.
//
// Lines that already contain ANSI sequences (the styled header, the selected
// row) are left untouched: the bubbles table wraps whole rows in a single
// style, and injecting another style mid-line would reset the row's
// background for the rest of the line.
func HighlightMatches(view, query string, start, end int) string {
	queryRunes := lowerRunes([]rune(query))
	if len(queryRunes) == 0 || end <= start || start < 0 {
		return view
	}

	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if strings.ContainsRune(line, '\x1b') {
			continue
		}
		runes := []rune(line)
		regionEnd := min(end, len(runes))
		if start >= regionEnd {
			continue
		}
		ranges := matchRanges(lowerRunes(runes[start:regionEnd]), queryRunes)
		if len(ranges) == 0 {
			continue
		}

		var sb strings.Builder
		sb.WriteString(string(runes[:start]))
		prev := start
		for _, r := range ranges {
			s, e := r[0]+start, r[1]+start
			sb.WriteString(string(runes[prev:s]))
			sb.WriteString(MatchHighlightStyle.Render(string(runes[s:e])))
			prev = e
		}
		sb.WriteString(string(runes[prev:]))
		lines[i] = sb.String()
	}
	return strings.Join(lines, "\n")
}

// matchRanges returns the [start, end) rune indexes of every non-overlapping
// occurrence of query in text. Both inputs must already be case-folded.
func matchRanges(text, query []rune) [][2]int {
	var out [][2]int
	if len(query) == 0 {
		return out
	}
	for i := 0; i+len(query) <= len(text); {
		matched := true
		for j := range query {
			if text[i+j] != query[j] {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, [2]int{i, i + len(query)})
			i += len(query)
		} else {
			i++
		}
	}
	return out
}

// lowerRunes folds rune-by-rune so indexes in the folded slice line up with
// the original (strings.ToLower can change the rune count for some locales).
func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}
