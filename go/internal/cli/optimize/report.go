package optimize

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// ReportTarget is the lightweight target view embedded in a report.
type ReportTarget struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Report is the rendered/serialized output of an optimize run.
type Report struct {
	Targets  []ReportTarget `json:"targets"`
	Findings []Finding      `json:"findings"`
}

// BuildReport assembles a Report from targets and findings.
func BuildReport(targets []Target, findings []Finding) Report {
	rt := make([]ReportTarget, 0, len(targets))
	for _, t := range targets {
		rt = append(rt, ReportTarget{Name: t.Name, Kind: t.Kind.String()})
	}
	return Report{Targets: rt, Findings: findings}
}

// Counts returns finding counts by severity plus the number of fixable findings.
func (r Report) Counts() (info, warning, errc, fixable int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityInfo:
			info++
		case SeverityWarning:
			warning++
		case SeverityError:
			errc++
		}
		if f.Fix != nil {
			fixable++
		}
	}
	return
}

// MaxSeverity returns the highest severity in the report (SeverityInfo if empty).
func (r Report) MaxSeverity() Severity {
	maxSev := SeverityInfo
	for _, f := range r.Findings {
		if f.Severity > maxSev {
			maxSev = f.Severity
		}
	}
	return maxSev
}

// Presentation styles. lipgloss disables color automatically on non-TTY
// output (and under NO_COLOR), so the rendered text degrades to clean plain
// text in pipes, CI, and tests — the tokens below stay intact either way.
var (
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(tui.ColorPrimary)
	titleStyle   = lipgloss.NewStyle().Bold(true)
	refStyle     = lipgloss.NewStyle().Foreground(tui.ColorDim)
	fixableStyle = lipgloss.NewStyle().Foreground(tui.ColorAccent)
	footerStyle  = lipgloss.NewStyle().Foreground(tui.ColorDim)
	nudgeStyle   = lipgloss.NewStyle().Bold(true).Foreground(tui.ColorAccent)

	severityStyles = map[Severity]lipgloss.Style{
		SeverityError:   lipgloss.NewStyle().Bold(true).Foreground(tui.ColorError),
		SeverityWarning: lipgloss.NewStyle().Foreground(tui.ColorNotice),
		SeverityInfo:    lipgloss.NewStyle().Foreground(tui.ColorInfo),
	}
	severityGlyphs = map[Severity]string{
		SeverityError:   "✖",
		SeverityWarning: "⚠",
		SeverityInfo:    "ℹ",
	}
)

// RenderHuman renders a report grouped by target. Output is colorized on a TTY
// and plain (with identical text tokens) when piped or captured.
func RenderHuman(r Report) string {
	var b strings.Builder

	refWidth := maxRefWidth(r.Findings)

	for _, t := range r.Targets {
		b.WriteString(headerStyle.Render(fmt.Sprintf("%s (%s)", t.Name, t.Kind)))
		b.WriteString("\n")
		for _, f := range r.Findings {
			if f.Target == t.Name {
				writeFindingLine(&b, f, refWidth)
			}
		}
	}
	// Defensive: any finding whose Target matches no listed target still gets shown.
	for _, f := range r.Findings {
		matched := false
		for _, t := range r.Targets {
			if f.Target == t.Name {
				matched = true
				break
			}
		}
		if !matched {
			writeFindingLine(&b, f, refWidth)
		}
	}

	info, warn, errc, fixable := r.Counts()
	total := info + warn + errc
	b.WriteString("\n")
	b.WriteString(footerStyle.Render(fmt.Sprintf("%d findings (%d errors, %d warnings, %d info)", total, errc, warn, info)))
	if fixable > 0 {
		b.WriteString(footerStyle.Render("  ·  "))
		b.WriteString(nudgeStyle.Render(fmt.Sprintf("%d fixable — run with --fix", fixable)))
	}
	b.WriteString("\n")
	return b.String()
}

// findingRef is the "analyzer:line" (or just "analyzer") reference for a finding.
func findingRef(f Finding) string {
	if f.Location != nil {
		return fmt.Sprintf("%s:%d", f.Analyzer, f.Location.Line)
	}
	return f.Analyzer
}

// maxRefWidth returns the widest reference string, for column alignment.
func maxRefWidth(findings []Finding) int {
	w := 0
	for _, f := range findings {
		if n := len(findingRef(f)); n > w {
			w = n
		}
	}
	return w
}

func writeFindingLine(b *strings.Builder, f Finding, refWidth int) {
	ref := findingRef(f)
	glyph := severityGlyphs[f.Severity]
	sevStyle := severityStyles[f.Severity]
	// "<glyph> <severity>" padded so titles line up across severities.
	marker := sevStyle.Render(fmt.Sprintf("%s %-7s", glyph, f.Severity.String()))
	// Pad the reference to refWidth (plain-text width) so titles align.
	paddedRef := refStyle.Render(ref) + strings.Repeat(" ", refWidth-len(ref))
	line := fmt.Sprintf("  %s  %s  %s", marker, paddedRef, titleStyle.Render(f.Title))
	if f.Fix != nil {
		line += "  " + fixableStyle.Render("(fixable)")
	}
	b.WriteString(line)
	b.WriteString("\n")
}
