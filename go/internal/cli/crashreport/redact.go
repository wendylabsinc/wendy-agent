// Package crashreport bundles, redacts, and submits opt-in diagnostic reports
// for unrecoverable failures.
package crashreport

import (
	"os"
	"regexp"
	"strings"
)

var (
	reBearer = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._+/=\-]+`)
	reToken  = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password)(["']?\s*[:=]\s*["']?)[^\s"',]+`)
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// reIPv6 matches compressed IPv6 (which must contain "::") and full 8-group form.
	// The "::"-requirement prevents false positives on clock timestamps like "12:34:56".
	// An optional zone ID suffix (%eth0) is also consumed to avoid partial leakage.
	reIPv6 = regexp.MustCompile(
		`(?:::[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{1,4}){0,6}` +
			`|[0-9a-fA-F]{1,4}(?::[0-9a-fA-F]{1,4}){0,5}::[0-9a-fA-F]{0,4}(?::[0-9a-fA-F]{1,4}){0,6}` +
			`|(?:[0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4})` +
			`(?:%[a-zA-Z0-9._-]+)?`,
	)
)

// Redact removes or masks sensitive data from a single string: the user's home
// directory, bearer tokens, key/secret assignments, emails, and IP addresses.
func Redact(s string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	s = reBearer.ReplaceAllString(s, "${1}<redacted>")
	s = reToken.ReplaceAllString(s, "${1}${2}<redacted>")
	s = reEmail.ReplaceAllString(s, "<redacted-email>")
	s = reIPv4.ReplaceAllString(s, "<redacted-ip>")
	s = reIPv6.ReplaceAllString(s, "<redacted-ip>")
	return s
}

// RedactLines applies Redact to each line.
func RedactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Redact(l)
	}
	return out
}

// RedactMap applies Redact to each value (keys are assumed safe field names).
func RedactMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = Redact(v)
	}
	return out
}
