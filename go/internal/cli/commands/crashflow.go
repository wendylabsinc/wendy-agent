package commands

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// MaybeRunCrashReport offers to submit a redacted diagnostic report after an
// unrecoverable failure. It is a strict no-op for recoverable errors, in CI,
// when disabled via WENDY_CRASHREPORT=false, or in a non-interactive terminal.
// It must never produce an error or alter the process exit code.
func MaybeRunCrashReport(ctx context.Context, executed *cobra.Command, err error, errorClass string) {
	if err == nil || diag.Classify(err) != diag.Unrecoverable {
		return
	}
	if env.IsCI() || !env.CrashReport() || !isInteractiveTerminal() {
		return
	}

	out := executed.ErrOrStderr()
	fmt.Fprintln(out, "\nThis looks like an unrecoverable failure.")
	if !crashPromptYesNo("Submit an anonymous, redacted diagnostic report to help us fix it?", false) {
		return
	}

	info := platforminfo.Collect()
	bundle := crashreport.Build(info, errorClass, string(diag.Unrecoverable), diag.Chain(err), diag.Recent(), buildOutputTail())
	bundle.Contact = "" // optional; left blank unless we later prompt for it

	fmt.Fprintln(out, "\nThe following (redacted) information will be sent:")
	fmt.Fprintln(out, info.Block())
	fmt.Fprintf(out, "Error: %s\n", bundle.ErrorChain)
	if !crashPromptYesNo("Send this report?", false) {
		fmt.Fprintln(out, "Report not sent.")
		return
	}

	client := dialDiagnosticsClient() // nil on failure → file fallback
	res, ferr := crashreport.Submit(ctx, client, bundle)
	if ferr != nil {
		fmt.Fprintf(out, "Could not save report: %v\n", ferr)
		return
	}
	if res.TrackingID != "" {
		fmt.Fprintf(out, "\nReport submitted. Tracking number: %s\n", res.TrackingID)
		if res.StatusURL != "" {
			fmt.Fprintf(out, "Track status: %s\n", res.StatusURL)
		}
		offerSubscribe(ctx, client, executed, res.TrackingID)
		return
	}
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
	fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/wendyos/issues")
}

// offerSubscribe asks whether to be notified when a release fixes the report.
// APNS device tokens come from the Mac app; the CLI subscribes without one and
// receives the fix via the cross-platform notification channel.
func offerSubscribe(ctx context.Context, client cloudpb.DiagnosticsServiceClient, executed *cobra.Command, trackingID string) {
	out := executed.ErrOrStderr()
	if !crashPromptYesNo("Notify me when a release fixes this?", true) {
		return
	}
	if _, err := crashreport.Subscribe(ctx, client, trackingID, "", ""); err != nil {
		fmt.Fprintf(out, "Could not subscribe now; you can still check %s later.\n", trackingID)
		return
	}
	fmt.Fprintln(out, "Subscribed. You'll see a notification on your next 'wendy' run once it's fixed.")
}

// dialDiagnosticsClient best-effort dials the cloud DiagnosticsService using the
// default auth session. Returns nil on any failure so Submit uses the file
// fallback. (Reuses dialCloudGRPC from cloud_tunnel.go.)
func dialDiagnosticsClient() cloudpb.DiagnosticsServiceClient {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	auth, ok := cfg.DefaultAuth()
	if !ok || auth == nil || auth.CloudGRPC == "" {
		return nil
	}
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil
	}
	return cloudpb.NewDiagnosticsServiceClient(conn)
}

// buildOutputTail returns recent build output lines for inclusion in a crash
// report. Returns nil for now; Task 12 wires real build output.
func buildOutputTail() []string {
	return nil
}

// crashPromptYesNo prints prompt with a [y/N] or [Y/n] suffix to stderr, reads
// one line from stdin, and returns the parsed answer. Empty input or a read
// error returns def.
func crashPromptYesNo(prompt string, def bool) bool {
	suffix := " [y/N] "
	if def {
		suffix = " [Y/n] "
	}
	fmt.Fprint(os.Stderr, prompt+suffix)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes")
}
