package mesh

import (
	"regexp"
	"strings"
)

// nonLabelRE matches any run of characters that are not valid in a DNS label.
var nonLabelRE = regexp.MustCompile(`[^a-z0-9]+`)

// Normalize canonicalizes a device name or org name to a single DNS label:
// lowercase, every run of non-[a-z0-9] characters collapsed to one hyphen,
// and leading/trailing hyphens trimmed. It is the single source of truth for
// how friendly names and org slugs are compared across the mesh.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonLabelRE.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
