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

	for _, src := range []Source{SourceLiteralIP, SourceMDNS, SourceDNS, SourceCache} {
		detail, ok := e.SourceResults[src]
		if !ok {
			continue
		}
		fmt.Fprintf(&sb, "  %-12s %s\n", string(src)+":", detail)
	}
	return strings.TrimRight(sb.String(), "\n")
}
