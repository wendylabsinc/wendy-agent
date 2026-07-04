package optimize

import "strings"

type releaseDebugAnalyzer struct{}

func (releaseDebugAnalyzer) ID() string { return "release-debug" }

func (a releaseDebugAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding

	wendyDebugDefined := false
	wendyDebugUsed := false

	for _, inst := range t.Dockerfile.Instructions {
		if (inst.Cmd == "ARG" || inst.Cmd == "ENV") && strings.Contains(inst.Args, "WENDY_DEBUG") {
			wendyDebugDefined = true
		}
		if inst.Cmd == "RUN" && strings.Contains(inst.Args, "WENDY_DEBUG") {
			wendyDebugUsed = true
		}

		if inst.Cmd != "RUN" {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]

		if strings.Contains(inst.Args, "swift build") &&
			!strings.Contains(inst.Args, "-c release") &&
			!strings.Contains(inst.Args, "--configuration release") {
			out = append(out, lineFlagFinding(a.ID(), t.Dockerfile.Path, inst.Line, raw,
				"swift build", "swift build -c release",
				"`swift build` defaults to a debug build. Add `-c release` so the shipped binary is optimized.",
				"add -c release to swift build"))
		}
		if strings.Contains(inst.Args, "cargo build") && !strings.Contains(inst.Args, "--release") {
			out = append(out, lineFlagFinding(a.ID(), t.Dockerfile.Path, inst.Line, raw,
				"cargo build", "cargo build --release",
				"`cargo build` defaults to a debug build. Add `--release` for an optimized binary.",
				"add --release to cargo build"))
		}
	}

	if wendyDebugDefined && !wendyDebugUsed {
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityInfo,
			Title:    "WENDY_DEBUG is declared but never used to toggle the build",
			Detail: "WENDY_DEBUG is defined but no RUN step branches on it. Gate the optimization level on it, " +
				"e.g. release by default and a debug build only when WENDY_DEBUG=1.",
			Location: nil,
		})
	}

	return out
}

// lineFlagFinding builds a warning whose fix appends a flag to a build command on a raw line.
func lineFlagFinding(analyzer, file string, line int, raw, oldCmd, newCmd, detail, fixDesc string) Finding {
	newLine := strings.Replace(raw, oldCmd, newCmd, 1)
	return Finding{
		Analyzer: analyzer,
		Severity: SeverityWarning,
		Title:    oldCmd + " is a debug build",
		Detail:   detail,
		Location: &Loc{File: file, Line: line},
		Fix: &Fix{
			Description: fixDesc,
			Op:          FixReplaceLine,
			File:        file,
			Line:        line,
			Old:         raw,
			New:         newLine,
		},
	}
}
