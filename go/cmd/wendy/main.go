package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/analytics"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/cli/memguard"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/cli/usermessage"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

func main() {
	start := time.Now()
	// Before anything else, so a leak during command construction or prerun is
	// caught too. A trip kills the process outright, which means no analytics
	// event and no error message from below — the heap profile it leaves behind
	// is the report.
	memguard.Start()
	cmd := commands.NewRootCmd()

	// A Bubble Tea program (device picker, spinners, progress bars) calls
	// signal.Notify then signal.Stop for SIGINT/SIGTERM on every run
	// (bubbletea's handleSignals). Stop only detaches its own channel — Go's
	// os/signal never restores the pre-Notify default (process-terminating)
	// disposition once Notify has been called, so after the first TUI exits,
	// a bare SIGINT is silently swallowed for the rest of the process and any
	// unbounded call made afterward (e.g. an RPC to a selected device) can
	// never be interrupted. Keeping our own listener registered for the whole
	// process lifetime, and threading its cancellation through every command's
	// context, keeps Ctrl+C effective no matter how many TUIs have already run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	executed, err := executeInvocation(ctx, cmd, os.Args[1:])
	if !isReadOnlyInvocation(executed) {
		trackCommand(executed, err, time.Since(start))
		commands.FinishInvocation(executed, err)
		analytics.Close()
	}
	exitCode := reportCommandResult(os.Stderr, err, executed, os.Args[1:], interactiveErrorTerminal())
	if !isReadOnlyInvocation(executed) {
		// After Close, whose delivered events can still record milestones: a sudo
		// run gives what it created in ~/.wendy and the cache back to the user.
		commands.HandBackSudoFiles()
	}

	// Windows: when this process owns its console window (UAC-relaunched or
	// double-clicked), exiting would close the window and destroy the output
	// above — hold it open until the user has read it.
	commands.PauseBeforeExitIfSoleConsole()
	os.Exit(exitCode)
}

// Initialize and record the invocation before Cobra's help/validation exits or
// a long-running command. Completion remains separate so a start never implies
// success. Recovery also covers startup; errors retain only canonical paths in
// telemetry, including an unknown subcommand's last recognized parent.
func executeInvocation(ctx context.Context, root *cobra.Command, args []string) (*cobra.Command, error) {
	if args == nil {
		args = []string{} // Cobra treats nil as a request to read os.Args.
	}
	root.SetContext(ctx)
	root.SetArgs(args)
	var target *cobra.Command
	executed, err := executeRecovering(func() (*cobra.Command, error) {
		target = commands.PrepareInvocationAnalytics(root, args)
		if err := commands.UnknownSubcommandError(args); err != nil {
			return target, err
		}
		return root.ExecuteC()
	})
	executed = commandFor(root, args, executed, err)
	if executed == nil {
		executed = target
	}
	return executed, err
}

// Diagnostics and explicit update checks must not initialize telemetry or touch
// configuration, including sudo ownership cleanup after command completion.
func isReadOnlyInvocation(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	if commands.InvocationAnalyticsExcluded(cmd) && cmd.CommandPath() != "wendy analytics disable" {
		return true
	}
	switch cmd.CommandPath() {
	case "wendy doctor":
		return true
	case "wendy update":
		check, _ := cmd.Flags().GetBool("check")
		return check
	}
	return false
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
func trackCommand(executed *cobra.Command, err error, dur time.Duration) {
	if executed == nil {
		return
	}
	path := executed.CommandPath()
	if path == "wendy __session-broker" || isReadOnlyInvocation(executed) || commands.InvocationAnalyticsExcluded(executed) {
		return
	}
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
		"attempt_id":   analytics.AttemptID(executed.Context()),
		"command_root": commandRoot(executed),
		"duration_ms":  strconv.FormatInt(dur.Milliseconds(), 10),
		"success":      strconv.FormatBool(err == nil),
		"is_dev_build": strconv.FormatBool(version.IsDev(version.Version)),
	}
	if err != nil {
		props["error_class"] = errorClass(err)
	}
	analytics.Track(event, props)

	// Activation milestones (one-time per install, best-effort). first_run and
	// first_real_command are only counted for genuine invocations, not the
	// Homebrew install artifact.
	if event == "command_executed" {
		analytics.TrackMilestoneOnce("first_run")
		if !isSetupInvocation(executed) {
			analytics.TrackMilestoneOnce("first_real_command")
		}
	}
	if m := milestoneFor(path, err == nil && !isSetupInvocation(executed)); m != "" {
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
		"wendy __ble-check", "wendy help", "wendy update":
		return true
	}
	return strings.HasPrefix(path, "wendy analytics") ||
		strings.HasPrefix(path, "wendy cache")
}

func isSetupInvocation(cmd *cobra.Command) bool {
	if !cmd.Runnable() || isSetupCommand(cmd.CommandPath()) {
		return true
	}
	help, _ := cmd.Flags().GetBool("help")
	versionOnly, _ := cmd.Flags().GetBool("version")
	return help || versionOnly
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
	case "wendy cloud login", "wendy auth login":
		return "auth_success"
	}
	return ""
}

// errorClass maps an execution error to a bounded enum suitable for analytics;
// see commands.ExecutionErrorClass, which the deploy_completed event shares.
// A recovered panic is classified here, where it is caught.
func errorClass(err error) string {
	var internal *internalError
	if errors.As(err, &internal) {
		return "internal_error"
	}
	return commands.ExecutionErrorClass(err)
}

// renderError and the JSON envelope share the CLI message boundary.
func renderError(err error) string {
	text := formatErrorParts(err).text
	if diagnostic, ok := commands.RenderIdentityRefusal(err, text); ok {
		return diagnostic
	}
	return tui.ErrorMessage(text)
}

type formattedError struct {
	text  string
	steps []string
}

func formatError(err error) error {
	return errors.New(formatErrorParts(err).text)
}

func formatErrorParts(err error) formattedError {
	r := usermessage.Explain(err, commands.ExecutionErrorClass(err))
	return formattedError{text: r.Text(), steps: r.Steps}
}
