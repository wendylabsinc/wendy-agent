package optimize

import (
	"fmt"
	"strings"
)

type buildCacheAnalyzer struct{}

func (buildCacheAnalyzer) ID() string { return "build-cache" }

// cacheRule maps a substring that signals a compiled-lang build to its cache mount target.
type cacheRule struct {
	match  string
	target string
}

var cacheRules = []cacheRule{
	{"cargo build", "/root/.cargo"},
	{"cargo fetch", "/root/.cargo"},
	{"go build", "/root/.cache/go-build"},
	{"go mod download", "/root/.cache/go-build"},
	{"swift build", "/root/.swiftpm"},
	{"npm install", "/root/.npm"},
	{"npm ci", "/root/.npm"},
	{"yarn install", "/root/.npm"},
	{"pnpm install", "/root/.npm"},
	{"pip install", "/root/.cache/pip"},
}

func (a buildCacheAnalyzer) Analyze(t *Target) []Finding {
	if t.Dockerfile == nil {
		return nil
	}
	var out []Finding
	for _, inst := range t.Dockerfile.Instructions {
		if inst.Cmd != "RUN" {
			continue
		}
		if hasCacheMount(inst.Flags) {
			continue
		}
		rule, ok := matchCacheRule(inst.Args)
		if !ok {
			continue
		}
		raw := t.Dockerfile.Lines[inst.Line-1]
		newLine := insertRunFlag(raw, fmt.Sprintf("--mount=type=cache,target=%s", rule.target))
		out = append(out, Finding{
			Analyzer: a.ID(),
			Severity: SeverityWarning,
			Title:    fmt.Sprintf("%q runs without a build cache mount", rule.match),
			Detail: fmt.Sprintf("Add `--mount=type=cache,target=%s` to this RUN so %s reuses its cache across builds, "+
				"cutting rebuild time substantially.", rule.target, rule.match),
			Location: &Loc{File: t.Dockerfile.Path, Line: inst.Line},
			Fix: &Fix{
				Description: fmt.Sprintf("add cache mount for %s", rule.match),
				Op:          FixReplaceLine,
				File:        t.Dockerfile.Path,
				Line:        inst.Line,
				Old:         raw,
				New:         newLine,
			},
		})
	}
	return out
}

func hasCacheMount(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--mount=") && strings.Contains(f, "type=cache") {
			return true
		}
	}
	return false
}

func matchCacheRule(args string) (cacheRule, bool) {
	for _, r := range cacheRules {
		if strings.Contains(args, r.match) {
			return r, true
		}
	}
	return cacheRule{}, false
}

// insertRunFlag inserts flag right after the leading RUN token of a raw line,
// preserving the line's leading indentation.
func insertRunFlag(raw, flag string) string {
	indent := raw[:len(raw)-len(strings.TrimLeft(raw, " \t"))]
	body := strings.TrimLeft(raw, " \t")
	const run = "RUN "
	if strings.HasPrefix(body, run) {
		return indent + run + flag + " " + strings.TrimPrefix(body, run)
	}
	return raw // not a simple RUN line; leave unchanged
}
