package commands

import (
	"fmt"
	"net/url"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

// reportBugIssueURL is the GitHub "new issue" endpoint for this repo. Keep the
// path casing exact — GitHub's own links use it and some proxies are case
// sensitive.
const reportBugIssueURL = "https://github.com/wendylabsinc/WendyOS/issues/new"

// reportBugURL builds a GitHub issue-form URL that pre-fills bug_report.yml's
// Versions and Host information fields via GitHub's documented per-field
// query-param prefill (field id as the parameter name). When bundle is
// non-nil (the automatic crash-report path), it also fills a short
// What-happened summary and, when localFile is set, a pointer to the
// locally-saved report file instead of the raw log tail. Component and
// Target hardware are never inferable, so they're left for the user to pick
// from the form's dropdowns.
func reportBugURL(info platforminfo.Info, bundle *crashreport.Bundle, localFile string) string {
	q := url.Values{}
	q.Set("template", "bug_report.yml")
	q.Set("version", info.CLIVersion)
	q.Set("host-os", info.Block())

	if bundle != nil {
		q.Set("what-happened", truncateForURL(bundle.ErrorClass+": "+bundle.ErrorChain, 500))
		if localFile != "" {
			q.Set("logs", "Full redacted diagnostic bundle saved locally at "+localFile+" — attach it here.")
		}
	}

	return reportBugIssueURL + "?" + q.Encode()
}

// truncateForURL shortens s to at most max runes (appending "…") so a single
// query value can't blow past practical URL-length limits.
func truncateForURL(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// lookPath is a seam so tests can simulate gh being present or absent without
// depending on the real PATH.
var lookPath = exec.LookPath

// maybeOpenReportBugURL opens rawURL in the browser when gh is on PATH (used
// only as a signal that this is an interactive developer machine — gh's own
// issue-create machinery is never invoked). Returns whether it succeeded;
// callers own the fallback message for the false case.
func maybeOpenReportBugURL(cmd *cobra.Command, rawURL string) bool {
	if _, err := lookPath("gh"); err != nil {
		return false
	}
	if err := openBrowser(rawURL); err != nil {
		return false
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Opening a pre-filled bug report in your browser...")
	return true
}
