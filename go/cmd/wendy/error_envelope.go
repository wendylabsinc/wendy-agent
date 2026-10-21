package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/cli/usermessage"
)

// errorEnvelope is the single line JSON mode writes to stderr for a failed
// command. Its shape is a published contract (see "Errors in JSON mode" in
// docs/clients/wendy-cli/global-flags.md): add fields, never rename or
// remove them.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string   `json:"code"`
	Exit      int      `json:"exit"`
	Message   string   `json:"message"`
	Retryable bool     `json:"retryable"`
	NextSteps []string `json:"next_steps"`
}

// reportFailure reports err the way the invocation asked for (a JSON envelope
// or styled text, both on stderr) and returns the process exit status.
// User cancellations print nothing. Tour children exit 130 so the parent
// can distinguish declining an install from completing it.
func reportFailure(stderr io.Writer, err error, executed *cobra.Command, args []string) int {
	if errors.Is(err, commands.ErrUserCancelled) {
		if os.Getenv("WENDY_TELEMETRY_ENTRYPOINT") == "tour" {
			return 130
		}
		return 0
	}
	if err == nil || errors.Is(err, commands.ErrDefaultCleared) {
		return 0
	}
	var reported *commands.ReportedExitError
	if errors.As(err, &reported) {
		return reported.ExitCode()
	}
	usermessage.Debug(err)
	class := errorClass(err)
	outcome := outcomeForClass(class)
	// Keep panic diagnostics behind the existing debug flag. JSON mode
	// keeps the envelope last even when a debug stack precedes it.
	var internal *internalError
	isInternal := errors.As(err, &internal)
	if commands.JSONErrorsRequested(executed, args) {
		if isInternal && os.Getenv("WENDY_TLS_DEBUG") != "" {
			fmt.Fprintf(stderr, "%s\n", bytes.TrimRight(internal.stack, "\n"))
		}
		if writeErrorEnvelope(stderr, newErrorEnvelope(err, class, outcome, executed)) == nil {
			return outcome.exit
		}
		// Encoding strings cannot realistically fail; if it does, fall
		// through so the failure is still reported, as text.
	}
	fmt.Fprintln(stderr, renderError(err))
	if isInternal && os.Getenv("WENDY_TLS_DEBUG") != "" {
		fmt.Fprintf(stderr, "\n%s", internal.stack)
	}
	return outcome.exit
}

func newErrorEnvelope(err error, class string, outcome errorOutcome, executed *cobra.Command) errorEnvelope {
	code := class
	if code == "" || code == "other" {
		code = "error"
	}
	message, steps := plainErrorReport(err)
	if class == "cli_usage" && len(steps) == 0 && executed != nil {
		steps = append(steps, fmt.Sprintf("Run '%s --help' for usage.", executed.CommandPath()))
	}
	return errorEnvelope{Error: errorBody{
		Code:      code,
		Exit:      outcome.exit,
		Message:   message,
		Retryable: outcome.retryable,
		NextSteps: steps,
	}}
}

// plainErrorReport returns err as the unstyled text renderError would show,
// minus the approved recovery steps, which it returns separately.
// Foreign descriptions are mapped; local messages retain their specific detail.
func plainErrorReport(err error) (string, []string) {
	r := usermessage.Explain(err, commands.ExecutionErrorClass(err))
	return strings.TrimSpace(ansi.Strip(r.Message)), cleanSteps(r.Steps)
}

// cleanSteps strips styling, drops blank and repeated steps, and never
// returns nil, so next_steps always encodes as a JSON array.
func cleanSteps(steps []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, step := range steps {
		step = strings.TrimSpace(ansi.Strip(step))
		if step == "" || seen[step] {
			continue
		}
		seen[step] = true
		out = append(out, step)
	}
	return out
}

// writeErrorEnvelope writes env as one line. HTML escaping is off so advice
// such as "ssh wendy@<host>" keeps its angle brackets as written.
func writeErrorEnvelope(w io.Writer, env errorEnvelope) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}
