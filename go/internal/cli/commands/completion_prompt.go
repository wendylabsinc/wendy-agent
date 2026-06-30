// Package commands - ambient prompt that offers to install shell completions
// when they are missing.
package commands

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// completionPromptInterval throttles the ambient completion prompt so an
// unanswered prompt (e.g. Ctrl+C) doesn't reappear on every invocation.
const completionPromptInterval = 24 * time.Hour

// completionGate captures everything that decides whether to show the ambient
// "install completions?" prompt. Keeping it a plain struct lets the decision be
// unit-tested without a TTY.
type completionGate struct {
	cfg         *config.Config
	now         time.Time
	interactive bool
	jsonOutput  bool
	firstRun    bool // analytics notice shown this invocation
	updateShown bool // a CLI-update prompt was surfaced this invocation
	exemptCmd   bool // command runs its own completion flow (completion*/tour) or is internal
}

// shouldPrompt reports whether the ambient completion prompt should be shown.
func (g completionGate) shouldPrompt() bool {
	switch {
	case !g.interactive, g.jsonOutput:
		// Never prompt in non-interactive or machine-readable contexts.
		return false
	case g.firstRun, g.updateShown:
		// Don't stack the prompt on top of the analytics notice or an update prompt.
		return false
	case g.exemptCmd:
		return false
	case g.cfg.CompletionInstalled, g.cfg.CompletionPromptDismissed:
		// Already installed, or the user said no once.
		return false
	default:
		return completionPromptDue(g.cfg, g.now)
	}
}

// completionPromptDue returns true when enough time has passed since the prompt
// was last shown. It mirrors dueCLIUpdateCheck.
func completionPromptDue(cfg *config.Config, now time.Time) bool {
	if cfg.LastCompletionPromptCheck == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, cfg.LastCompletionPromptCheck)
	if err != nil {
		return true
	}
	if t.After(now) {
		// Stored timestamp is in the future (clock skew or manual edit); treat as due.
		return true
	}
	return now.Sub(t) >= completionPromptInterval
}

// completionExemptCmd reports whether cmd handles completions itself (the
// `completion` command tree and `tour`) or is an internal command that should
// never prompt.
func completionExemptCmd(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "tour", "__ble-check", "__usb-setup", "open-browser":
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "completion" {
			return true
		}
	}
	return false
}

// maybePromptInstallCompletions offers, at most once per throttle window, to
// install shell completions when they are missing. It is best-effort: any error
// (config I/O, cancelled prompt, failed install) is reported but never fails the
// command the user actually ran.
func maybePromptInstallCompletions(cmd *cobra.Command, firstRun, updateShown bool) {
	cfg, err := config.Load()
	if err != nil {
		return
	}

	gate := completionGate{
		cfg:         cfg,
		now:         time.Now().UTC(),
		interactive: isInteractiveTerminal(),
		jsonOutput:  jsonOutput,
		firstRun:    firstRun,
		updateShown: updateShown,
		exemptCmd:   completionExemptCmd(cmd),
	}
	if !gate.shouldPrompt() {
		return
	}

	// Record that we showed the prompt now, so an unanswered prompt (Ctrl+C)
	// doesn't reappear until the throttle window elapses.
	cfg.LastCompletionPromptCheck = gate.now.Format(time.RFC3339)
	if err := config.Save(cfg); err != nil {
		return
	}

	cmd.PrintErrln("\nShell completions for `wendy` aren't installed yet.")
	confirmed, promptErr := tui.ConfirmNoDefault("Install them now?", tea.WithOutput(os.Stderr))
	if promptErr != nil {
		// Cancelled / EOF / error: leave the dismiss flag unset so the prompt
		// may reappear after the throttle window.
		return
	}
	if !confirmed {
		cfg.CompletionPromptDismissed = true
		_ = config.Save(cfg)
		cmd.PrintErrln("Okay — run `wendy completion install` anytime to enable them.")
		return
	}

	if err := installCompletionsForCurrentShell(cmd.Root(), cmd.ErrOrStderr()); err != nil {
		cmd.PrintErrf("Could not install completions: %v\n", err)
		cmd.PrintErrln("Run `wendy completion install` to try again.")
	}
}
