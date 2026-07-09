package analytics

import (
	"regexp"
	"strings"
)

// maxErrorDetailRunes caps the redacted detail so a pathological error can't
// bloat the payload or the analytics store, and bounds residual leak surface.
const maxErrorDetailRunes = 200

const redactedToken = "[redacted]"

// Redaction patterns, applied in order. This is best-effort PII scrubbing for
// the error_detail field: it strips the common ways a Go error string carries
// user- or environment-specific data (paths, hosts, addresses, emails) while
// leaving the structural, non-sensitive text (errno phrases, fmt prefixes,
// versions, device-type slugs) intact. Patterns are adapted from the agent-side
// dmesg redactor.
var (
	// URLs first, so a host embedded in a URL doesn't survive as a bare FQDN.
	reURL = regexp.MustCompile(`https?://\S+`)

	// IPv6: a hex group followed by two or more ":hex" groups (empty groups
	// allow the "::" compressed form). This also catches colon-form MAC
	// addresses, which is fine — they are redacted either way.
	reIPv6 = regexp.MustCompile(`[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{0,4}){2,}`)

	// Email before FQDN so the domain part is consumed together with the
	// local part (stripping the domain first would leave the username behind).
	reEmail = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)

	// Bare dotted hostnames / FQDNs. Requires an alphabetic TLD, so dotted
	// version strings like "0.10.4" (digit-terminated) are not matched.
	reFQDN = regexp.MustCompile(`\b([A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,}\b`)

	// IPv4 dotted quad.
	reIPv4 = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)

	// Hyphen-form MAC (colon-form is covered by reIPv6 above).
	reMACHyphen = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}-){5}[0-9A-Fa-f]{2}\b`)

	// Windows absolute path (drive letter + backslash run).
	reWinPath = regexp.MustCompile(`[A-Za-z]:\\[^\s]*`)

	// Unix absolute path: a "/" that begins a path — anchored with \B so a "/"
	// preceded by a word char (e.g. the "/" in "input/output") is NOT treated
	// as a path root — followed by one or more "/segment" runs. Covers home,
	// device, and temp paths (/Users, /home, /root, /dev, /tmp, /var, …).
	reUnixPath = regexp.MustCompile(`\B/[A-Za-z0-9._-]+(?:/[A-Za-z0-9._-]+)*`)
)

// RedactErrorDetail returns a best-effort PII-scrubbed, length-capped copy of an
// error message suitable for the error_detail analytics field. It is not a
// security boundary: it removes the common shapes of sensitive data, not every
// conceivable one. Returns "" for an empty input.
func RedactErrorDetail(msg string) string {
	if msg == "" {
		return ""
	}
	for _, re := range []*regexp.Regexp{
		reURL,
		reIPv6,
		reEmail,
		reFQDN,
		reIPv4,
		reMACHyphen,
		reWinPath,
		reUnixPath,
	} {
		msg = re.ReplaceAllString(msg, redactedToken)
	}
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > maxErrorDetailRunes {
		msg = strings.TrimSpace(string(r[:maxErrorDetailRunes]))
	}
	return msg
}
