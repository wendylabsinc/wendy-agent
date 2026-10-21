package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
)

// Review 4339052765: a recovered bug needs its diagnostic and reporting step.
func TestReviewPanicExplainsBugAndHowToReport(t *testing.T) {
	_, err := executeRecovering(func() (*cobra.Command, error) { panic("nil map write") })
	message, steps := plainErrorReport(err)
	if !strings.Contains(message, "Internal error: nil map write") || len(steps) != 1 || steps[0] != internalErrorStep {
		t.Errorf("lost panic diagnosis: %q %q", message, steps)
	}
}

// Review 4339052773: JSON suggestions are steps, not part of the message.
func TestReviewUnknownCommandKeepsSeparateSuggestionSteps(t *testing.T) {
	message, steps := plainErrorReport(commands.UnknownSubcommandError([]string{"device", "inf"}))
	if len(steps) != 2 || steps[0] != "Did you mean 'wendy device info'?" || strings.Contains(message, "Did you mean") || strings.Contains(message, "--help") {
		t.Errorf("suggestions buried in message: %q %q", message, steps)
	}
}

func TestReviewLocalRecoveryStepPrintedOnce(t *testing.T) {
	step := "Confirm it with 'wendy device get-default'."
	err := steppedError{cause: errors.New("default device could not be reached"), steps: []string{step}}
	if got := renderError(err); strings.Count(got, step) != 1 {
		t.Errorf("duplicated recovery step: %q", got)
	}
}
