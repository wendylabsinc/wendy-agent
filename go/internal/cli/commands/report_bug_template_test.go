package commands

import (
	"os"
	"regexp"
	"testing"
)

// TestBugReportTemplateFieldIDsMatchReportBugURL is a guard-rail: reportBugURL
// (report_bug.go) hardcodes the query-param names "version", "host-os",
// "what-happened", and "logs" to match bug_report.yml's form field ids, so
// GitHub's per-field URL prefill actually lands in the right boxes. Nothing
// else catches a drift between the two — if someone renames a field id in
// the YAML (or in reportBugURL) without updating the other, the prefill
// silently does nothing for that field. This test reads the raw YAML bytes
// and checks the ids it depends on are still present, without adding a YAML
// parsing dependency.
func TestBugReportTemplateFieldIDsMatchReportBugURL(t *testing.T) {
	const templatePath = "../../../../.github/ISSUE_TEMPLATE/bug_report.yml"

	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("reading %s: %v", templatePath, err)
	}

	idRe := regexp.MustCompile(`(?m)^\s*id:\s*(\S+)\s*$`)
	matches := idRe.FindAllSubmatch(data, -1)
	ids := make(map[string]bool, len(matches))
	for _, m := range matches {
		ids[string(m[1])] = true
	}

	// These are the exact field ids reportBugURL targets via GitHub's
	// per-field query-param prefill. "template" is deliberately excluded: it
	// is GitHub's own template-selector query param, not a form field id
	// declared in this YAML.
	want := []string{"version", "host-os", "what-happened", "logs"}
	for _, id := range want {
		if !ids[id] {
			t.Errorf("bug_report.yml is missing field id %q, which reportBugURL depends on for prefill", id)
		}
	}
}
