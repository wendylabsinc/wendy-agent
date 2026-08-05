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

// reportBugOpenResult distinguishes why maybeOpenReportBugURL did not
// successfully open a browser, so callers can give the user accurate
// guidance instead of assuming gh is missing.
type reportBugOpenResult int

const (
	reportBugOpened reportBugOpenResult = iota
	reportBugNoGH
	reportBugOpenFailed
)

// maybeOpenReportBugURL opens rawURL in the browser when gh is on PATH (used
// only as a signal that this is an interactive developer machine — gh's own
// issue-create machinery is never invoked). Returns which of the three
// outcomes occurred; callers own the fallback message for the non-opened
// cases, which differ (gh missing vs. gh present but the open itself failed).
func maybeOpenReportBugURL(cmd *cobra.Command, rawURL string) reportBugOpenResult {
	if _, err := lookPath("gh"); err != nil {
		return reportBugNoGH
	}
	if err := openBrowser(rawURL); err != nil {
		return reportBugOpenFailed
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Opening a pre-filled bug report in your browser...")
	return reportBugOpened
}

// newReportBugCmd builds the hidden `wendy report-bug` command: it opens a
// pre-filled GitHub issue in the browser when `gh` is on PATH, or prints the
// URL (plus the platform info block) as a fallback otherwise.
func newReportBugCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "report-bug",
		Short:  "Open a pre-filled GitHub bug report",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := platforminfo.Collect()
			rawURL := reportBugURL(info, nil, "")
			switch maybeOpenReportBugURL(cmd, rawURL) {
			case reportBugOpened:
				return nil
			case reportBugOpenFailed:
				out := cmd.OutOrStdout()
				fmt.Fprintln(out, "Could not open the browser automatically. Open this link to file a report:")
				fmt.Fprintln(out, rawURL)
				fmt.Fprintln(out)
				fmt.Fprintln(out, info.Block())
				return nil
			default: // reportBugNoGH
				out := cmd.OutOrStdout()
				fmt.Fprintln(out, "`gh` CLI not found — install it from https://cli.github.com, or open this link to file a report:")
				fmt.Fprintln(out, rawURL)
				fmt.Fprintln(out)
				fmt.Fprintln(out, info.Block())
				return nil
			}
		},
	}
}
