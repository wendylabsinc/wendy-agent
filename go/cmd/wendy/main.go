package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	start := time.Now()
	cmd := commands.NewRootCmd()
	executed, err := cmd.ExecuteC()
	trackCommand(executed, err, time.Since(start))
	analytics.Close()

	if err != nil {
		if errors.Is(err, commands.ErrUserCancelled) || errors.Is(err, commands.ErrDefaultCleared) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, tui.ErrorMessage(formatError(err).Error()))
		os.Exit(1)
	}
}

// trackCommand emits a single analytics event describing the invocation.
// Properties:
//
//   - command_name: canonical cobra path (e.g. "wendy device wifi connect"),
//     never flag values or positional args.
//   - command_root: top-level token (e.g. "device") for low-cardinality
//     breakdowns that survive PostHog's 25-row table cap.
//   - duration_ms: wall-clock time from process start.
//   - success: bool serialized as "true"/"false".
//   - is_dev_build: true for development builds (version.IsDev) — the local
//     "dev" default or a CI branch build with a "-dev" suffix.
//   - error_class (only when err != nil): bounded enum derived from err —
//     never the error message text, which can leak hostnames or paths.
//   - error_detail (only for catch-all classes "other"/"grpc_other"): a
//     best-effort redacted excerpt of the error message, so failures that
//     carry no specific reason code are still diagnosable. See
//     analytics.RedactErrorDetail — this is deliberately relaxed from the
//     error_class no-message-text rule, scoped to the unclassified buckets.
func trackCommand(executed *cobra.Command, err error, dur time.Duration) {
	if executed == nil {
		return
	}
	path := executed.CommandPath()
	// Homebrew exports HOMEBREW_PREFIX/HOMEBREW_CELLAR/HOMEBREW_REPOSITORY into
	// every interactive shell once `eval "$(brew shellenv)"` is set up (the
	// standard ~/.zprofile line), so env presence alone cannot distinguish the
	// post-install hook from a user manually running `wendy completion
	// install` in a normal Homebrew terminal. The post-install hook runs with
	// a non-interactive stdin (no TTY), so require both.
	homebrewPostInstall := env.IsHomebrewInstall() && !stdinIsInteractive()
	event := eventNameFor(path, homebrewPostInstall)
	props := map[string]string{
		"command_name": path,
		"command_root": commandRoot(executed),
		"duration_ms":  strconv.FormatInt(dur.Milliseconds(), 10),
		"success":      strconv.FormatBool(err == nil),
		"is_dev_build": strconv.FormatBool(version.IsDev(version.Version)),
	}
	if err != nil {
		class := errorClass(err)
		props["error_class"] = class
		// For catch-all buckets the class alone says nothing about what
		// happened, so attach a best-effort redacted detail. Classified
		// buckets (install_* reasons, grpc_unavailable, context_*,
		// user_cancelled, …) are self-describing and get no detail, keeping
		// data collection and PII surface minimal.
		if class == "other" || class == "grpc_other" {
			if detail := analytics.RedactErrorDetail(err.Error()); detail != "" {
				props["error_detail"] = detail
			}
		}
	}
	analytics.Track(event, props)

	// Activation milestones (one-time per install, best-effort). first_run and
	// first_real_command are only counted for genuine invocations, not the
	// Homebrew install artifact.
	if event == "command_executed" {
		analytics.TrackMilestoneOnce("first_run")
		if !isSetupCommand(path) {
			analytics.TrackMilestoneOnce("first_real_command")
		}
	}
	if m := milestoneFor(path, err == nil); m != "" {
		analytics.TrackMilestoneOnce(m)
	}
}

func commandRoot(c *cobra.Command) string {
	if c == nil {
		return ""
	}
	if !c.HasParent() {
		return c.Name()
	}
	for c.Parent() != nil && c.Parent().HasParent() {
		c = c.Parent()
	}
	return c.Name()
}

// eventNameFor returns the analytics event name for a command invocation.
// homebrewPostInstall must mean the Homebrew post-install context
// specifically (Homebrew env present AND stdin non-interactive), not merely
// that Homebrew env vars are set — see trackCommand. A Homebrew post-install
// `wendy completion install` is reported as install_completed so it is not
// counted as deliberate CLI usage.
func eventNameFor(commandPath string, homebrewPostInstall bool) string {
	if homebrewPostInstall && commandPath == "wendy completion install" {
		return "install_completed"
	}
	return "command_executed"
}

// stdinIsInteractive reports whether stdin is attached to a terminal. The
// Homebrew post-install hook runs wendy with a non-interactive stdin.
func stdinIsInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// isSetupCommand reports whether a command path is a meta/setup command that
// does not represent deliberate product use. The first "real" command is the
// first invocation whose path is not one of these.
func isSetupCommand(path string) bool {
	switch path {
	case "wendy completion install",
		"wendy completion bash", "wendy completion zsh",
		"wendy completion fish", "wendy completion powershell",
		"wendy __complete", "wendy __completeNoDesc",
		"wendy __ble-check", "wendy help":
		return true
	}
	return strings.HasPrefix(path, "wendy analytics") ||
		strings.HasPrefix(path, "wendy cache")
}

// milestoneFor maps a successful command invocation to a one-time activation
// milestone event name, or "" if the command is not a milestone.
func milestoneFor(commandPath string, success bool) string {
	if !success {
		return ""
	}
	switch commandPath {
	case "wendy discover":
		return "discover_success"
	case "wendy init":
		return "init_success"
	case "wendy run":
		return "first_deploy_success"
	case "wendy cloud login", "wendy auth login":
		return "auth_success"
	}
	return ""
}

// errorClass maps an execution error to a bounded enum suitable for analytics.
// It must never embed the error message, which can contain hostnames, paths,
// or other user input. (The free-text detail lives in a separate, redacted
// error_detail property set by trackCommand — never here.)
//
// User-cancellation sentinels are checked first so an outer wrap never
// reclassifies them, then cancellation/deadline, then any command-supplied
// commands.ReasonError code (see commands.WithReason), then gRPC codes. gRPC errors are extracted via status.FromError, which
// walks the wrapped chain — substring matching on err.Error() would miss
// errors wrapped via fmt.Errorf with a custom prefix or any future change to
// grpc-go's stringification.
func errorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, commands.ErrUserCancelled) || errors.Is(err, commands.ErrDefaultCleared) {
		return "user_cancelled"
	}
	// Cancellation and deadline are checked before any command-supplied reason
	// so an install_* wrap of a cancelled/timed-out operation is never
	// mislabeled as an install failure.
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "context_deadline"
	}
	// A command may tag a failure with a bounded reason code (see
	// commands.WithReason), which is more specific than the gRPC/"other"
	// fallbacks below. gRPC transport errors are not wrapped this way, so this
	// never hides grpc_unavailable/grpc_deadline/etc.
	var re *commands.ReasonError
	if errors.As(err, &re) && re.Reason != "" {
		return re.Reason
	}
	// status.FromError returns ok=true only for real gRPC errors (those
	// produced by the grpc package or implementing GRPCStatus()). For
	// non-gRPC errors it returns ok=false with a synthesized Unknown code,
	// which we don't want to claim as a gRPC failure. An explicit
	// Unknown code from a real gRPC error, however, should still bucket
	// under grpc_other.
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		switch st.Code() {
		case codes.Canceled:
			return "context_canceled"
		case codes.DeadlineExceeded:
			return "grpc_deadline"
		case codes.Unavailable:
			return "grpc_unavailable"
		case codes.Unimplemented:
			return "grpc_unimplemented"
		default:
			return "grpc_other"
		}
	}
	return "other"
}

func formatError(err error) error {
	msg := err.Error()
	if !strings.Contains(msg, "rpc error: code = ") {
		return err
	}

	// Extract the context prefix (e.g. "starting agent update: ") before the rpc error.
	prefix := ""
	if idx := strings.Index(msg, "rpc error: code = "); idx > 0 {
		prefix = msg[:idx]
	}

	isPKICoreCall := strings.Contains(prefix, "pki-core")
	isCloudCall := strings.Contains(prefix, "issuing certificate") ||
		strings.Contains(prefix, "refreshing certificate") ||
		strings.Contains(prefix, "creating enrollment token") ||
		strings.Contains(prefix, "connecting to cloud")

	isTLSError := strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "bad certificate") ||
		strings.Contains(msg, "authentication handshake failed") ||
		strings.Contains(msg, "certificate required")

	switch {
	case strings.Contains(msg, "code = Unavailable") && isTLSError && !isPKICoreCall && !isCloudCall:
		return fmt.Errorf("%sTLS handshake rejected by device (possible clock skew or cert mismatch).\n  Check the device clock: ssh wendy@<host> 'timedatectl status'\n  For full TLS details rerun with WENDY_TLS_DEBUG=1", prefix)
	case strings.Contains(msg, "code = Unavailable") && strings.Contains(msg, "connection refused"):
		if isPKICoreCall {
			return fmt.Errorf("%sCould not connect to local pki-core. Check that the gRPC endpoint is reachable from this machine.", prefix)
		}
		if isCloudCall {
			return fmt.Errorf("%sCould not connect to Wendy Cloud. Please try again later.", prefix)
		}
		return fmt.Errorf("%sCould not connect to device. Is it powered on and connected to the network?", prefix)
	case strings.Contains(msg, "code = Unavailable"):
		if isPKICoreCall {
			return fmt.Errorf("%sLocal pki-core is unavailable.", prefix)
		}
		if isCloudCall {
			return fmt.Errorf("%sWendy Cloud is unavailable. Please try again later.", prefix)
		}
		// Preserve the server's description when it provides actionable
		// detail (e.g. "WiFi management is not available (nmcli not found)").
		// Only fall back to the generic message for transport-level errors
		// that lack a useful desc.
		if idx := strings.Index(msg, "desc = "); idx >= 0 {
			desc := msg[idx+len("desc = "):]
			return fmt.Errorf("%s%s", prefix, desc)
		}
		return fmt.Errorf("%sDevice is unavailable.", prefix)
	case strings.Contains(msg, "code = DeadlineExceeded"):
		return fmt.Errorf("%sConnection timed out.", prefix)
	case strings.Contains(msg, "code = Unimplemented"):
		// Preserve intentional, contextual Unimplemented descriptions from the
		// agent (for example Wendy Agent for Mac feature gaps). Keep the legacy
		// update hint only for generic protocol-mismatch responses where gRPC or
		// an old agent did not recognize the service/method.
		if desc, ok := grpcDesc(msg); ok && !isGenericUnimplementedDesc(desc) {
			return fmt.Errorf("%s%s", prefix, desc)
		}
		return fmt.Errorf("%sNot supported by this agent version. Try updating the agent.", prefix)
	default:
		// Strip transport noise, keep the desc message.
		if desc, ok := grpcDesc(msg); ok {
			return fmt.Errorf("%s%s", prefix, desc)
		}
		return err
	}
}

func grpcDesc(msg string) (string, bool) {
	idx := strings.Index(msg, "desc = ")
	if idx < 0 {
		return "", false
	}
	desc := strings.TrimSpace(msg[idx+len("desc = "):])
	return desc, desc != ""
}

func isGenericUnimplementedDesc(desc string) bool {
	lower := strings.ToLower(strings.TrimSpace(desc))
	if lower == "" {
		return true
	}
	return strings.HasPrefix(lower, "unknown service ") ||
		strings.HasPrefix(lower, "unknown method ") ||
		(strings.HasPrefix(lower, "method ") && strings.HasSuffix(lower, " not implemented"))
}
