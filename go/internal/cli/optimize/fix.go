package optimize

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// AppliedFix records the outcome of applying one Fix.
type AppliedFix struct {
	Fix     Fix
	Applied bool
	Reason  string // populated when Applied is false
}

// ApplyFixes applies every finding's non-nil Fix. It is idempotent.
//
// FixReplaceLine fixes are grouped by file and applied together (see
// applyLineFixes) rather than one read-modify-write cycle per fix, so that
// two fixes landing on the same line both take effect instead of the second
// one silently losing to a stale Fix.Old check. Contradictory same-line
// pairs are resolved first: a pip cache mount supersedes --no-cache-dir.
func ApplyFixes(findings []Finding) ([]AppliedFix, error) {
	var results []AppliedFix
	var lineFixes []Finding

	for _, f := range findings {
		if f.Fix == nil {
			continue
		}
		switch f.Fix.Op {
		case FixCreateFile:
			fx := *f.Fix
			if fileExists(fx.File) {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "file already exists"})
				continue
			}
			if err := os.WriteFile(fx.File, []byte(fx.New), 0o644); err != nil {
				return results, fmt.Errorf("creating %s: %w", fx.File, err)
			}
			results = append(results, AppliedFix{Fix: fx, Applied: true})

		case FixReplaceLine:
			lineFixes = append(lineFixes, f)

		default:
			results = append(results, AppliedFix{Fix: *f.Fix, Applied: false, Reason: "unknown fix op"})
		}
	}

	byFile := map[string][]Finding{}
	var fileOrder []string
	for _, f := range lineFixes {
		file := f.Fix.File
		if _, seen := byFile[file]; !seen {
			fileOrder = append(fileOrder, file)
		}
		byFile[file] = append(byFile[file], f)
	}

	for _, file := range fileOrder {
		data, err := os.ReadFile(file)
		if err != nil {
			return results, fmt.Errorf("reading %s: %w", file, err)
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		trailingNL := strings.HasSuffix(text, "\n")
		body := text
		if trailingNL {
			body = strings.TrimSuffix(text, "\n")
		}
		lines := strings.Split(body, "\n")

		newLines, applied := applyLineFixes(lines, byFile[file])
		results = append(results, applied...)

		changed := false
		for i := range lines {
			if lines[i] != newLines[i] {
				changed = true
				break
			}
		}
		if !changed {
			continue
		}
		out := strings.Join(newLines, "\n")
		if trailingNL {
			out += "\n"
		}
		if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
			return results, fmt.Errorf("writing %s: %w", file, err)
		}
	}
	return results, nil
}

// safeAutoApplyAnalyzers is the subset of analyzer IDs whose Fix only ever
// adds a flag or mount to an existing instruction without changing what
// actually gets installed or how the built artifact behaves, so it's safe
// to apply automatically and silently (e.g. before every build, with no
// prompt). Fixes outside this set are reported but never auto-applied:
// apt-install's --no-install-recommends changes which packages land in the
// image (Recommends-pulled runtime deps like ca-certificates simply
// disappear), and release-debug's -c release/--release swap changes the
// shipped binary's runtime behavior — both are visible-diff, explicit
// `--fix` candidates only. (node-ci goes further and emits no Fix at all:
// an npm-ci swap can turn a passing build into a hard failure on a drifted
// lockfile.)
var safeAutoApplyAnalyzers = map[string]bool{
	"pip-flags":   true,
	"build-cache": true,
	"add-copy":    true,
}

// SafeAutoApplyFindings filters findings to the subset eligible for silent,
// unconfirmed application — see safeAutoApplyAnalyzers.
func SafeAutoApplyFindings(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Fix != nil && safeAutoApplyAnalyzers[f.Analyzer] {
			out = append(out, f)
		}
	}
	return out
}

// ApplyFixesToLines applies every FixReplaceLine fix in findings to an
// in-memory copy of lines, without touching any file on disk — the
// in-memory analogue of ApplyFixes' FixReplaceLine handling, including the
// same same-line composition (see applyLineFixes). FixCreateFile fixes are
// skipped; they create a separate file, not a line within lines.
func ApplyFixesToLines(lines []string, findings []Finding) ([]string, []AppliedFix) {
	var lineFixes []Finding
	for _, f := range findings {
		if f.Fix != nil && f.Fix.Op == FixReplaceLine {
			lineFixes = append(lineFixes, f)
		}
	}
	return applyLineFixes(lines, lineFixes)
}

// applyLineFixes applies a set of FixReplaceLine fixes (already known to
// share one file) to an in-memory copy of lines, composing multiple fixes
// that land on the same line instead of letting all but the first lose to
// a stale Fix.Old check.
//
// Each fix's Old/New pair is diffed to recover exactly what text it
// inserts and at what byte offset into the line's pristine text (see
// deriveInsertion). Fixes that turn out to be a pure insertion — add text,
// remove nothing — compose freely: applying them right-to-left by that
// offset means an earlier insertion's position is never invalidated by a
// later one. A fix that isn't a pure insertion (e.g. add-copy's ADD ->
// COPY, a prefix swap) falls back to the original whole-line-equality
// behavior and is applied on its own; in practice this never collides with
// an insertion fix, since ADD lines never also match an apt/pip/build-cache
// rule.
func applyLineFixes(lines []string, findings []Finding) ([]string, []AppliedFix) {
	out := append([]string(nil), lines...)
	var results []AppliedFix

	// A build-cache pip mount and a pip-flags --no-cache-dir landing on the
	// same line contradict each other: --no-cache-dir disables exactly the
	// cache the mount persists. The mount alone keeps the layer just as slim
	// (the cache lives outside the image) while letting rebuilds reuse
	// downloaded wheels, so it supersedes the flag.
	pipMountLines := map[int]bool{}
	for _, f := range findings {
		if f.Analyzer == "build-cache" && strings.Contains(f.Fix.New, "--mount=type=cache,target=/root/.cache/pip") {
			pipMountLines[f.Fix.Line] = true
		}
	}

	byLine := map[int][]Fix{}
	var order []int
	for _, f := range findings {
		if f.Analyzer == "pip-flags" && pipMountLines[f.Fix.Line] {
			results = append(results, AppliedFix{Fix: *f.Fix, Applied: false, Reason: "superseded by pip cache mount on the same line"})
			continue
		}
		idx := f.Fix.Line - 1
		if _, seen := byLine[idx]; !seen {
			order = append(order, idx)
		}
		byLine[idx] = append(byLine[idx], *f.Fix)
	}

	type insertion struct {
		fix      Fix
		pos      int
		inserted string
	}

	for _, idx := range order {
		fixes := byLine[idx]
		if idx < 0 || idx >= len(out) {
			for _, fx := range fixes {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "line out of range"})
			}
			continue
		}
		current := out[idx]

		var inserts []insertion
		var others []Fix
		for _, fx := range fixes {
			if pos, ins, ok := deriveInsertion(fx.Old, fx.New); ok {
				inserts = append(inserts, insertion{fix: fx, pos: pos, inserted: ins})
			} else {
				others = append(others, fx)
			}
		}

		for _, fx := range others {
			if current != fx.Old {
				results = append(results, AppliedFix{Fix: fx, Applied: false, Reason: "already applied or line changed"})
				continue
			}
			current = fx.New
			results = append(results, AppliedFix{Fix: fx, Applied: true})
		}

		allMatch := len(inserts) > 0
		for _, ins := range inserts {
			if current != ins.fix.Old {
				allMatch = false
				break
			}
		}
		if allMatch {
			sort.Slice(inserts, func(i, j int) bool { return inserts[i].pos > inserts[j].pos })
			for _, ins := range inserts {
				current = current[:ins.pos] + ins.inserted + current[ins.pos:]
			}
			for _, ins := range inserts {
				results = append(results, AppliedFix{Fix: ins.fix, Applied: true})
			}
		} else {
			for _, ins := range inserts {
				results = append(results, AppliedFix{Fix: ins.fix, Applied: false, Reason: "already applied or line changed"})
			}
		}

		out[idx] = current
	}
	return out, results
}

// deriveInsertion recovers, from a FixReplaceLine's Old/New pair, the byte
// offset into Old where New's extra text begins and what that text is —
// but only when New is Old with something purely ADDED (nothing removed or
// replaced). Returns ok=false for a fix that swaps one substring for
// another (e.g. "ADD" -> "COPY"), which can't be composed with another
// fix on the same line via simple position-based splicing.
func deriveInsertion(old, new string) (pos int, inserted string, ok bool) {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	if old[p:len(old)-s] != "" {
		return 0, "", false
	}
	return p, new[p : len(new)-s], true
}
