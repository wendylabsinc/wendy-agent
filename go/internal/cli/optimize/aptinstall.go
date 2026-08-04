package optimize

import (
	"fmt"
	"strings"
)

type aptInstallAnalyzer struct{}

func (aptInstallAnalyzer) ID() string { return "apt-install" }

// findAptInstall reports the apt install invocation present in args, if any.
func findAptInstall(args string) (string, bool) {
	for _, cmd := range []string{"apt-get install", "apt install"} {
		if strings.Contains(args, cmd) {
			return cmd, true
		}
	}
	return "", false
}

func (a aptInstallAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" {
			continue
		}
		aptCmd, ok := findAptInstall(inst.Args)
		if !ok {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]

		if !strings.Contains(inst.Args, "apt-get update") {
			out = append(out, Finding{
				Analyzer: a.ID(),
				Severity: SeverityWarning,
				Title:    fmt.Sprintf("%q runs in a RUN layer without apt-get update", aptCmd),
				Detail: "apt-get update and apt-get install must run in the same RUN instruction. Docker " +
					"caches each RUN independently, so a later apt-get install can silently reuse a stale " +
					"package index from an old update layer, causing \"unable to locate package\" failures.",
				Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
			})
		}

		if !strings.Contains(inst.Args, "--no-install-recommends") {
			f := Finding{
				Analyzer: a.ID(),
				Severity: SeverityWarning,
				Title:    fmt.Sprintf("%q runs without --no-install-recommends", aptCmd),
				Detail:   "Recommended (but not required) packages inflate the image. Add --no-install-recommends to skip them.",
				Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
			}
			// Only offer a fix when the command lives on this single physical
			// line — a backslash-continued RUN would need editing a different
			// line than the one we can see here.
			if strings.Contains(raw, aptCmd) {
				f.Fix = &Fix{
					Description: fmt.Sprintf("add --no-install-recommends to %s", aptCmd),
					Op:          FixReplaceLine,
					File:        t.Dockerfile.Path,
					Line:        inst.Line,
					Old:         raw,
					New:         strings.Replace(raw, aptCmd, aptCmd+" --no-install-recommends", 1),
				}
			}
			out = append(out, f)
		}
	}
	return out
}
