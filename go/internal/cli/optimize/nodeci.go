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

// canReplaceNPMInstall is intentionally narrower than detection. npm ci can
// accept the two dependency-tree flags npm explicitly documents as needing to
// match npm install; other install flags are left for a human to evaluate.
func canReplaceNPMInstall(args string) bool {
	fields := strings.Fields(args)
	if len(fields) < 2 || fields[0] != "npm" || fields[1] != "install" {
		return false
	}
	for i := 2; i < len(fields) && !isShellSeparator(fields[i]); i++ {
		switch fields[i] {
		case "--legacy-peer-deps", "--install-links":
		default:
			return false
		}
	}
	return true
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
		raw := t.Dockerfile.Lines[inst.Line-1]
		f := Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    "npm install ignores package-lock.json",
			Detail: "A package-lock.json is present, but this build uses npm install, which can update it. " +
				"Use npm ci to install exactly the locked versions and fail fast on a drifted lockfile.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}
		// Only rewrite when npm starts the RUN command (after Dockerfile
		// instruction flags). More complex shell commands are reported but
		// left for the user to edit.
		if canReplaceNPMInstall(inst.Args) && strings.Contains(raw, "npm install") {
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
