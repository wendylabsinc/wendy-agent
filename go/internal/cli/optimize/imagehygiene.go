package optimize

import (
	"fmt"
	"strings"
)

type imageHygieneAnalyzer struct{}

func (imageHygieneAnalyzer) ID() string { return "image-hygiene" }

func (a imageHygieneAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	stageNames := map[string]bool{}

	for _, inst := range t.Dockerfile.Instructions {
		switch inst.Cmd {
		case "FROM":
			out = append(out, a.analyzeFrom(t, inst, stageNames)...)
		case "CMD", "ENTRYPOINT":
			out = append(out, a.analyzeShellForm(t, inst)...)
		case "COPY":
			out = append(out, a.analyzeCopyFrom(t, inst)...)
		}
	}
	return out
}

func (a imageHygieneAnalyzer) analyzeFrom(t *Target, inst Instruction, stageNames map[string]bool) []Finding {
	fields := strings.Fields(inst.Args)
	if len(fields) == 0 {
		return nil
	}
	ref := fields[0]
	if len(fields) >= 3 && strings.EqualFold(fields[1], "AS") {
		stageNames[fields[2]] = true
	}
	if strings.EqualFold(ref, "scratch") {
		return nil
	}
	if stageNames[ref] || strings.Contains(ref, "@") {
		return nil // references a prior build stage, or is digest-pinned
	}

	lastSeg := ref
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		lastSeg = ref[idx+1:]
	}
	if !strings.Contains(lastSeg, ":") {
		return []Finding{{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("FROM %s has no tag (defaults to :latest)", ref),
			Detail:   "An untagged image resolves to :latest, which drifts over time and breaks reproducible builds. Pin an explicit version.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}}
	}
	tag := lastSeg[strings.Index(lastSeg, ":")+1:]
	if tag == "latest" {
		return []Finding{{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("FROM %s is not pinned to a version", ref),
			Detail:   ":latest drifts over time — the same Dockerfile can produce a different image tomorrow. Pin an explicit version tag.",
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
		}}
	}
	return nil
}

func (a imageHygieneAnalyzer) analyzeShellForm(t *Target, inst Instruction) []Finding {
	if strings.HasPrefix(strings.TrimSpace(inst.Args), "[") {
		return nil // exec form
	}
	return []Finding{{
		Analyzer: a.ID(),
		Severity: SeverityWarning,
		Title:    fmt.Sprintf("%s uses shell form", inst.Cmd),
		Detail: fmt.Sprintf("Shell-form %s runs under /bin/sh -c, so the shell (not your process) is PID 1. "+
			"Signals like SIGTERM don't reach your app, so `docker stop` hangs until the kill timeout. Use exec "+
			"form (e.g. %s [\"...\"]) — but only after confirming the command doesn't rely on shell features "+
			"(pipes, redirects, globs, $VAR expansion) that exec form won't provide.", inst.Cmd, inst.Cmd),
		Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
	}}
}

func (a imageHygieneAnalyzer) analyzeCopyFrom(t *Target, inst Instruction) []Finding {
	fromsStage := false
	for _, fl := range inst.Flags {
		if strings.HasPrefix(fl, "--from=") {
			fromsStage = true
			break
		}
	}
	if !fromsStage {
		return nil
	}
	fields := strings.Fields(inst.Args)
	if len(fields) == 0 || fields[0] != "/" {
		return nil
	}
	return []Finding{{
		Analyzer: a.ID(),
		Severity: SeverityWarning,
		Title:    "COPY --from copies the entire build stage",
		Detail:   "Copying \"/\" from a build stage drags in its compilers, package caches, and source tree. Copy only the specific artifact path(s) the runtime stage needs.",
		Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
	}}
}
