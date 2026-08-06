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
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    "npm install ignores package-lock.json",
			Detail: "A package-lock.json is present, but this build uses npm install, which can update it. " +
				"Consider npm ci to install exactly the locked versions and fail fast on a drifted lockfile — " +
				"not auto-fixed here, since a drifted lockfile would turn a slow build into a broken one.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		})
	}
	return out
}
