package optimize

import (
	"path/filepath"
	"strings"
)

type nodeCIAnalyzer struct{}

func (nodeCIAnalyzer) ID() string { return "node-ci" }

func isShellSeparator(token string) bool {
	switch token {
	case "&&", "||", ";", "|":
		return true
	default:
		return false
	}
}

// hasProjectNPMInstall reports whether args contains an exact `npm install`
// command that installs the current project. Invocations with positional
// arguments install individual dependencies and cannot be replaced by npm ci.
func hasProjectNPMInstall(args string) bool {
	fields := strings.Fields(args)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] != "npm" || fields[i+1] != "install" ||
			(i > 0 && !isShellSeparator(fields[i-1])) {
			continue
		}
		projectInstall := true
		for j := i + 2; j < len(fields) && !isShellSeparator(fields[j]); j++ {
			arg := fields[j]
			if arg == "-g" || strings.HasPrefix(arg, "-g=") ||
				arg == "--global" || strings.HasPrefix(arg, "--global=") {
				projectInstall = false
				break
			}
			// A non-flag argument is a package spec (or an ambiguous separated
			// flag value), so do not suggest a semantics-changing rewrite.
			if !strings.HasPrefix(arg, "-") {
				projectInstall = false
				break
			}
		}
		if projectInstall {
			return true
		}
	}
	return false
}

func (a nodeCIAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	if !fileExists(filepath.Join(t.Dir, "package-lock.json")) {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" || !hasProjectNPMInstall(inst.Args) {
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
