package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// steppedError stands in for commands' next-steps error: steps appended to
// the message in the usual layout, and exposed through NextSteps.
type steppedError struct {
	cause error
	steps []string
}

func (e steppedError) Error() string {
	return e.cause.Error() + "\n  " + strings.Join(e.steps, "\n  ")
}
func (e steppedError) Unwrap() error       { return e.cause }
func (e steppedError) NextSteps() []string { return e.steps }

// decodeEnvelope asserts out is exactly one line of JSON and decodes it.
func decodeEnvelope(t *testing.T, out string) errorBody {
	t.Helper()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("stderr is not exactly one line: %q", out)
	}
	var env errorEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, out)
	}
	return env.Error
}

func TestReportFailure_UserCancellationIsSilentSuccess(t *testing.T) {
	t.Setenv("WENDY_TELEMETRY_ENTRYPOINT", "")
	for _, err := range []error{nil, commands.ErrUserCancelled, commands.ErrDefaultCleared} {
		var out strings.Builder
		if code := reportFailure(&out, err, nil, []string{"--json"}); code != 0 || out.Len() != 0 {
			t.Errorf("reportFailure(%v) = %d, %q; want 0 and no output", err, code, out.String())
		}
	}
}

func TestReportedExitIsSilentAndTrackedAsFailure(t *testing.T) {
	clearCIEnv(t)
	events := captureAnalytics(t)
	root := &cobra.Command{Use: "wendy"}
	project := &cobra.Command{Use: "project"}
	command := &cobra.Command{Use: "optimize"}
	root.AddCommand(project)
	project.AddCommand(command)
	err := fmt.Errorf("optimization report: %w", &commands.ReportedExitError{})

	trackCommand(command, err, time.Millisecond)
	if len(*events) != 1 || (*events)[0].event != "command_executed" ||
		(*events)[0].props["command_name"] != "wendy project optimize" ||
		(*events)[0].props["success"] != "false" || (*events)[0].props["error_class"] != "other" {
		t.Fatalf("reported failure telemetry = %+v", *events)
	}
	for _, args := range [][]string{{"--json"}, {"--json=false"}} {
		var stderr strings.Builder
		if code := reportFailure(&stderr, err, command, args); code != 1 || stderr.Len() != 0 {
			t.Errorf("reportFailure(%q) = %d, %q; want 1 and no stderr", args, code, stderr.String())
		}
	}
}

func TestReportFailure_JSONEnvelope(t *testing.T) {
	var out strings.Builder
	err := fmt.Errorf("listing cloud devices: %w", config.ErrNotLoggedIn)
	code := reportFailure(&out, err, nil, []string{"--json", "cloud", "discover"})
	if code != 3 {
		t.Errorf("exit = %d, want 3", code)
	}
	got := decodeEnvelope(t, out.String())
	want := errorBody{
		Code:      "auth_required",
		Exit:      3,
		Message:   "Sign in again to continue.",
		NextSteps: []string{"Run 'wendy auth login', then try again."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("envelope = %+v, want %+v", got, want)
	}
	if !strings.Contains(out.String(), `"next_steps":[`) {
		t.Errorf("next_steps must encode as an array even when empty: %s", out.String())
	}
}

// Human and machine modes share approved prose and retain exit semantics.
func TestReportFailure_TextModeUsesApprovedSentences(t *testing.T) {
	prevProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(prevProfile) })
	for _, tc := range []struct {
		err     error
		exit    int
		message string
	}{
		{fmt.Errorf("listing cloud devices: %w", config.ErrNotLoggedIn), 3, "Sign in again to continue."},
		{errors.Join(errors.New("api: build failed\nstep 3/7 exited 1"), errors.New("worker: build failed")), 1, "Something went wrong. Try again."},
		{fmt.Errorf("connecting: %w", diagnosticTestError{}), 1, "Something went wrong. Try again."},
	} {
		var out strings.Builder
		if got := reportFailure(&out, tc.err, nil, []string{"--json=false"}); got != tc.exit {
			t.Errorf("exit = %d", got)
		}
		if !strings.HasPrefix(out.String(), "✗ "+tc.message) || strings.Contains(out.String(), tc.err.Error()) {
			t.Errorf("unsafe text = %q", out.String())
		}
	}
}

func TestReportFailure_UsageErrorsExitTwoAndPointAtHelp(t *testing.T) {
	root := &cobra.Command{Use: "wendy"}
	info := &cobra.Command{Use: "info", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(info)

	var out strings.Builder
	err := errors.New(`unknown command "banana" for "wendy"`)
	if code := reportFailure(&out, err, root, []string{"--json", "banana"}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	got := decodeEnvelope(t, out.String())
	if got.Code != "cli_usage" || got.Retryable || !reflect.DeepEqual(got.NextSteps, []string{"Run 'wendy --help' for usage."}) {
		t.Errorf("envelope = %+v", got)
	}
}

func TestReportFailure_StepsLeaveTheMessage(t *testing.T) {
	var out strings.Builder
	err := steppedError{
		cause: fmt.Errorf("default device %q is set but could not be reached: %w", "127.0.0.1:1", dialedError{"127.0.0.1:1", refusedDial}),
		steps: []string{"Confirm it with 'wendy device get-default'."},
	}
	reportFailure(&out, err, nil, []string{"--json"})
	got := decodeEnvelope(t, out.String())
	wantMessage := "Could not connect to device at 127.0.0.1:1. Make sure it's on and connected, then try again."
	if got.Message != wantMessage {
		t.Errorf("message = %q, want %q", got.Message, wantMessage)
	}
	if !reflect.DeepEqual(got.NextSteps, []string{}) {
		t.Errorf("next_steps = %q", got.NextSteps)
	}
}

// Review focus: advice text keeps its angle brackets, and a styled
// diagnostic is reported without its ✗ marker or colour.
func TestReportFailure_EnvelopeTextIsPlain(t *testing.T) {
	var out strings.Builder
	tlsErr := steppedError{
		cause: fmt.Errorf("querying device version: %w", status.Error(codes.Unavailable,
			`connection error: desc = "transport: authentication handshake failed: remote error: tls: bad certificate"`)),
		steps: []string{"Retry with --device <host>."},
	}
	reportFailure(&out, tlsErr, nil, []string{"--json"})
	if strings.Contains(out.String(), "Retry with --device <host>.") || strings.Contains(out.String(), "u003c") {
		t.Errorf("advice was HTML-escaped or lost: %s", out.String())
	}
	got := decodeEnvelope(t, out.String())
	if got.Message != "The device rejected the secure connection. Its clock or certificates may need attention." || len(got.NextSteps) != 2 {
		t.Errorf("envelope = %+v", got)
	}

	out.Reset()
	reportFailure(&out, fmt.Errorf("connecting: %w", diagnosticTestError{}), nil, []string{"--json"})
	got = decodeEnvelope(t, out.String())
	if got.Message != "Something went wrong. Try again." || strings.ContainsAny(got.Message, "✗\x1b") {
		t.Errorf("diagnostic message = %q, want the plain Error() text", got.Message)
	}
}

// Review focus: multi-line and joined errors still produce exactly one line.
func TestReportFailure_MultiLineErrorsStayOneLine(t *testing.T) {
	var out strings.Builder
	err := errors.Join(errors.New("api: build failed\nstep 3/7 exited 1"), errors.New("worker: build failed"))
	reportFailure(&out, err, nil, []string{"--json"})
	got := decodeEnvelope(t, out.String())
	if got.Message != "Something went wrong. Try again." || got.Code != "error" || got.Exit != 1 {
		t.Errorf("envelope = %+v", got)
	}
}

// inlineAdviceError stands in for errors whose advice is part of the
// message rather than an indented line, such as the provisioned-agent
// "Unauthorized. Run 'wendy auth login' ..." error.
type inlineAdviceError struct{ steps []string }

func (e inlineAdviceError) Error() string {
	return "Unauthorized. " + e.steps[0] + "\nLast mTLS error: tls: expired certificate\n" + e.steps[1]
}
func (e inlineAdviceError) NextSteps() []string { return e.steps }

// Advice that sits in the message must reach next_steps, and leave the
// message, whatever its layout.
func TestReportFailure_AdviceInTheMessageBecomesNextSteps(t *testing.T) {
	var out strings.Builder
	reportFailure(&out, commands.UnknownSubcommandError([]string{"device", "inf"}), nil, []string{"--json", "device", "inf"})
	got := decodeEnvelope(t, out.String())
	if got.Message != "The command or its arguments are not valid. Check the command and try again." ||
		!reflect.DeepEqual(got.NextSteps, []string{}) {
		t.Errorf("unknown subcommand envelope = %+v", got)
	}

	out.Reset()
	login := "Run 'wendy auth login' with an account that can access this provisioned wendy-agent."
	refresh := "Your stored certificates may be outdated. Run 'wendy auth refresh-certs' to re-issue them."
	reportFailure(&out, fmt.Errorf("connecting: %w", inlineAdviceError{[]string{login, refresh}}), nil, []string{"--json"})
	got = decodeEnvelope(t, out.String())
	if got.Message != "The device rejected the secure connection. Its clock or certificates may need attention." ||
		len(got.NextSteps) != 2 {
		t.Errorf("unauthorized envelope = %+v", got)
	}
}
