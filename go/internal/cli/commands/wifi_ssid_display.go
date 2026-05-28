package commands

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

func quoteSSIDForPrompt(ssid string) string {
	return strconv.Quote(sanitizeSSIDForDisplay(ssid))
}

func sanitizeSSIDForDisplay(ssid string) string {
	var b strings.Builder
	for _, r := range ssid {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f || isBidiControl(r) {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isBidiControl(r rune) bool {
	return r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}
