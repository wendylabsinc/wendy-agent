package resolution

import (
	"fmt"
	"strings"
)

// ResolutionError is returned by Resolve when every strategy produced zero
// candidates. It carries per-strategy detail for human-readable output.
type ResolutionError struct {
	Target        string
	SourceResults map[Source]string // strategy name → human description of result
}

func (e *ResolutionError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "could not reach %q:\n", e.Target)

	// Find the longest source key present in the map.
	maxKeyLen := 0
	for _, src := range []Source{SourceLiteralIP, SourceMDNS, SourceDNS, SourceCache} {
		if _, ok := e.SourceResults[src]; ok {
			keyLen := len(string(src)) + 1 // +1 for the colon
			if keyLen > maxKeyLen {
				maxKeyLen = keyLen
			}
		}
	}

	// Print in order, padding keys to align values.
	for _, src := range []Source{SourceLiteralIP, SourceMDNS, SourceDNS, SourceCache} {
		detail, ok := e.SourceResults[src]
		if !ok {
			continue
		}
		keyWithColon := string(src) + ":"
		padding := maxKeyLen - len(keyWithColon) + 1 // +1 for space after padding
		fmt.Fprintf(&sb, "  %s%s%s\n", keyWithColon, strings.Repeat(" ", padding), detail)
	}
	return strings.TrimRight(sb.String(), "\n")
}
