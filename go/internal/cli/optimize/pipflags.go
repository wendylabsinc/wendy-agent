package optimize

import (
	"fmt"
	"strings"
)

type pipFlagsAnalyzer struct{}

func (pipFlagsAnalyzer) ID() string { return "pip-flags" }

func findPipInstall(args string) (string, bool) {
	for _, cmd := range []string{"pip3 install", "pip install"} {
		if strings.Contains(args, cmd) {
			return cmd, true
		}
	}
	return "", false
}

func (a pipFlagsAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" {
			continue
		}
		pipCmd, ok := findPipInstall(inst.Args)
		if !ok || strings.Contains(inst.Args, "--no-cache-dir") {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]
		f := Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("%q runs without --no-cache-dir", pipCmd),
			Detail:   "pip keeps a wheel cache under ~/.cache/pip by default, bloating the image layer. Add --no-cache-dir to skip it.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}
		if strings.Contains(raw, pipCmd) {
			f.Fix = &Fix{
				Description: fmt.Sprintf("add --no-cache-dir to %s", pipCmd),
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         strings.Replace(raw, pipCmd, pipCmd+" --no-cache-dir", 1),
			}
		}
		out = append(out, f)
	}
	return out
}
