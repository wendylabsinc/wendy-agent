package optimize

import (
	"slices"
	"strings"
)

type addCopyAnalyzer struct{}

func (addCopyAnalyzer) ID() string { return "add-copy" }

var addArchiveExtensions = []string{".tar", ".tar.gz", ".tgz", ".tar.bz2", ".tar.xz", ".zip"}

// isAddSpecialCase reports whether src uses one of ADD's unique behaviors
// (remote URL fetch or local tar auto-extraction) that COPY cannot replicate.
func isAddSpecialCase(src string) bool {
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return true
	}
	for _, ext := range addArchiveExtensions {
		if strings.HasSuffix(src, ext) {
			return true
		}
	}
	return false
}

func (a addCopyAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "ADD" {
			continue
		}
		// Skip JSON array form (ADD ["src", "dst"]) since strings.Fields can't safely
		// detect URLs/archives there.
		if strings.HasPrefix(strings.TrimSpace(inst.Args), "[") {
			continue
		}
		fields := strings.Fields(inst.Args)
		if len(fields) < 2 {
			continue
		}
		// All fields but the last are sources; skip if any relies on an
		// ADD-only behavior COPY can't reproduce.
		if slices.ContainsFunc(fields[:len(fields)-1], isAddSpecialCase) {
			continue
		}

		raw := t.Dockerfile.Lines[inst.Line-1]
		f := Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "ADD used where COPY would do",
			Detail: "This ADD only copies local, non-archive files — none of ADD's remote-fetch or " +
				"tar-auto-extraction behavior is in play. COPY is more explicit and avoids ADD's surprising " +
				"auto-extract semantics if the source ever gains a .tar extension later.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}
		if strings.HasPrefix(strings.TrimLeft(raw, " \t"), "ADD ") {
			indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
			body := strings.TrimLeft(raw, " \t")
			f.Fix = &Fix{
				Description: "replace ADD with COPY",
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         indent + "COPY " + strings.TrimPrefix(body, "ADD "),
			}
		}
		out = append(out, f)
	}
	return out
}
