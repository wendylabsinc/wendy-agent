package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

type crashConsent int

const (
	consentSkip crashConsent = iota
	consentSend
	consentSuppress
)

// analyticsEnabledFn is a seam for tests to stub analytics.Enabled without
// depending on the real analytics opt-in state.
var analyticsEnabledFn = analytics.Enabled

// MaybeRunCrashReport offers to submit a redacted diagnostic report after an
// unrecoverable failure. Strict no-op for recoverable errors, in CI, when
// analytics is disabled, when suppressed, or non-interactively. Never errors
// or changes the exit code.
func MaybeRunCrashReport(ctx context.Context, executed *cobra.Command, err error, errorClass string) {
	if err == nil || diag.Classify(err) != diag.Unrecoverable {
		return
	}
	if env.IsCI() || !env.CrashReport() || !analyticsEnabledFn() || !isInteractiveTerminal() {
		return
	}
	cfg, cerr := config.Load()
	if cerr != nil {
		return
	}
	if cfg.CrashReport != nil && cfg.CrashReport.Suppressed {
		return
	}

	out := executed.ErrOrStderr()
	fmt.Fprintln(out, "\nThis looks like an unrecoverable failure.")
	switch crashConsentPrompt("Submit an anonymous, redacted diagnostic report to help us fix it?") {
	case consentSuppress:
		setCrashSuppressed(cfg)
		fmt.Fprintln(out, "Okay — we won't ask again. Re-enable with 'wendy analytics enable' semantics later.")
		return
	case consentSkip:
		return
	case consentSend:
	}

	info := platforminfo.Collect()
	bundle := crashreport.Build(info, errorClass, string(diag.Unrecoverable), diag.Chain(err), diag.Recent(), buildOutputTail())

	fmt.Fprintln(out, "\nThe following (redacted) information will be sent:")
	fmt.Fprintln(out, info.Block())
	fmt.Fprintf(out, "Error class: %s\n", bundle.ErrorClass)
	fmt.Fprintf(out, "Severity: %s\n", bundle.Severity)
	fmt.Fprintf(out, "Error: %s\n", bundle.ErrorChain)
	printTail(out, "Recent log lines:", bundle.LogTail)
	printTail(out, "Build output:", bundle.BuildOutputTail)

	if !crashPromptYesNo("Send this report?", false) {
		fmt.Fprintln(out, "Report not sent.")
		return
	}
	notify := crashPromptYesNo("Notify me when a release fixes this?", true)

	anonID, aerr := analytics.DistinctID()
	if aerr != nil {
		fmt.Fprintln(out, "Could not prepare report.")
		return
	}
	endpoint := analytics.TelemetryBaseURL() + "/crashreports"
	res, ferr := crashreport.SubmitHTTP(ctx, endpoint, anonID, bundle, notify)
	if ferr != nil {
		fmt.Fprintf(out, "Could not save report: %v\n", ferr)
		return
	}
	if res.TrackingID != "" {
		fmt.Fprintf(out, "\nReport submitted. Tracking number: %s\n", res.TrackingID)
		if res.StatusURL != "" {
			fmt.Fprintf(out, "Track status: %s\n", res.StatusURL)
		}
		if notify {
			addSubscribedReport(cfg, res.TrackingID)
			fmt.Fprintln(out, "You'll see a note on your next 'wendy' run once it's fixed.")
		}
		return
	}
	reportCrashLocally(executed, out, info, bundle, res.LocalFile)
}

// reportCrashLocally is the cloud-unavailable fallback: point the user at a
// pre-filled GitHub issue (the same mechanism as `wendy report-bug`) carrying
// the redacted bundle's error summary and the local report path, instead of
// the previous dead-end "attach it to an issue" instruction.
func reportCrashLocally(cmd *cobra.Command, out io.Writer, info platforminfo.Info, bundle crashreport.Bundle, localFile string) {
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", localFile)
	rawURL := reportBugURL(info, &bundle, localFile)
	if !maybeOpenReportBugURL(cmd, rawURL) {
		fmt.Fprintln(out, "Open a bug report:", rawURL)
	}
}

func printTail(out io.Writer, header string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(out, "\n"+header)
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
}

func setCrashSuppressed(cfg *config.Config) {
	if cfg.CrashReport == nil {
		cfg.CrashReport = &config.CrashReportConfig{}
	}
	cfg.CrashReport.Suppressed = true
	_ = config.Save(cfg)
}

func addSubscribedReport(cfg *config.Config, trackingID string) {
	if cfg.CrashReport == nil {
		cfg.CrashReport = &config.CrashReportConfig{}
	}
	cfg.CrashReport.SubscribedReports = append(cfg.CrashReport.SubscribedReports, trackingID)
	_ = config.Save(cfg)
}

// buildOutputTail returns recent build output lines for a report. Nil for now.
func buildOutputTail() []string { return nil }

// crashConsentPrompt prints a 3-way prompt and returns the parsed choice.
func crashConsentPrompt(prompt string) crashConsent {
	fmt.Fprint(os.Stderr, prompt+" [y]es / [n]o / [d]on't ask again: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return consentSkip
	}
	return parseCrashConsent(line)
}

func parseCrashConsent(line string) crashConsent {
	s := strings.ToLower(strings.TrimSpace(line))
	switch {
	case s == "y" || s == "yes":
		return consentSend
	case strings.HasPrefix(s, "d"):
		return consentSuppress
	default:
		return consentSkip
	}
}

// crashPromptYesNo prints a [y/N] or [Y/n] prompt and returns the answer.
func crashPromptYesNo(prompt string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	fmt.Fprint(os.Stderr, prompt+suffix)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}
