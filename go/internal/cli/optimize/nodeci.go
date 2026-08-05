package optimize

import (
	"path/filepath"
	"strings"
)

type nodeCIAnalyzer struct{}

func (nodeCIAnalyzer) ID() string { return "node-ci" }

func (a nodeCIAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	if !fileExists(filepath.Join(t.Dir, "package-lock.json")) {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" || !strings.Contains(inst.Args, "npm install") {
			continue
		}
		if strings.Contains(inst.Args, "-g") || strings.Contains(inst.Args, "--global") {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]
		f := Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    "npm install ignores package-lock.json",
			Detail: "A package-lock.json is present, but this build uses npm install, which can update it. " +
				"Use npm ci to install exactly the locked versions and fail fast on a drifted lockfile.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}
		if strings.Contains(raw, "npm install") {
			f.Fix = &Fix{
				Description: "replace npm install with npm ci",
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         strings.Replace(raw, "npm install", "npm ci", 1),
			}
		}
		out = append(out, f)
	}
	return out
}
