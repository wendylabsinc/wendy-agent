package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// An unrecovered panic used to exit 2, the same status as a usage error,
// with Go's trace instead of an envelope.
func TestExecuteRecoveringTurnsAPanicIntoAnInternalError(t *testing.T) {
	executed, err := executeRecovering(func() (*cobra.Command, error) {
		panic("boom")
	})
	if executed != nil {
		t.Errorf("executed = %v, want nil after a panic", executed)
	}
	var internal *internalError
	if !errors.As(err, &internal) {
		t.Fatalf("err = %v, want an internal error", err)
	}
	if got := errorClass(err); got != "internal_error" {
		t.Errorf("errorClass = %q, want internal_error", got)
	}
	if !strings.Contains(string(internal.stack), "panic_test.go") {
		t.Errorf("stack does not show where the panic happened:\n%s", internal.stack)
	}

	// No panic: the result passes through untouched.
	root := &cobra.Command{Use: "wendy"}
	want := errors.New("plain failure")
	if executed, err := executeRecovering(func() (*cobra.Command, error) { return root, want }); executed != root || err != want {
		t.Errorf("executeRecovering = %v, %v; want the command and error unchanged", executed, err)
	}
}

func TestReportFailure_InternalError(t *testing.T) {
	_, err := executeRecovering(func() (*cobra.Command, error) { panic(errors.New("nil map write")) })
	for _, debug := range []string{"", "1"} {
		t.Setenv("WENDY_TLS_DEBUG", debug)
		for _, args := range [][]string{{"--json"}, {"--json=false"}} {
			var out strings.Builder
			if code := reportFailure(&out, err, nil, args); code != 70 {
				t.Errorf("exit = %d, want 70", code)
			}
			if debug == "" {
				if strings.Contains(out.String(), "goroutine ") {
					t.Errorf("raw panic reached normal output: %s", out.String())
				}
			} else if !strings.Contains(out.String(), "goroutine ") || !strings.Contains(out.String(), "panic_test.go") {
				t.Errorf("debug stack missing: %s", out.String())
			}
			if args[0] == "--json" {
				_, envelope, _ := cutLastLine(out.String())
				got := decodeEnvelope(t, envelope)
				if got.Code != "internal_error" || got.Exit != 70 || got.Retryable || got.Message != "Internal error: nil map write." || len(got.NextSteps) != 1 || got.NextSteps[0] != internalErrorStep {
					t.Errorf("envelope = %+v", got)
				}
			} else if !strings.Contains(out.String(), "Internal error: nil map write") || strings.Count(out.String(), internalErrorStep) != 1 {
				t.Errorf("message missing: %s", out.String())
			}
		}
	}
}

// cutLastLine splits out into everything before its last line and that last
// line (with its newline); ok is false when out has fewer than two lines.
func cutLastLine(out string) (before, last string, ok bool) {
	trimmed := strings.TrimSuffix(out, "\n")
	i := strings.LastIndex(trimmed, "\n")
	if i < 0 {
		return "", out, false
	}
	return out[:i+1], out[i+1:], true
}

// A panic leaves ExecuteC without a command, but the failure must still be
// tracked, and a hidden helper that panics must still report as text to the
// parent wendy that reads its stderr.
func TestCommandForAPanicIsTheOneTheArgumentsName(t *testing.T) {
	root := &cobra.Command{Use: "wendy", SilenceErrors: true, SilenceUsage: true}
	helper := &cobra.Command{Use: "__bmap-write", RunE: func(*cobra.Command, []string) error { panic("boom") }}
	helper.Flags().String("device", "", "")
	root.AddCommand(helper)
	args := []string{"__bmap-write", "--device", "/dev/disk4"}
	root.SetArgs(args)

	executed, err := executeRecovering(root.ExecuteC)
	if executed != nil {
		t.Fatalf("executed = %v, want nil straight after a panic", executed)
	}
	if got := commandFor(root, args, executed, err); got != helper {
		t.Fatalf("commandFor = %v, want the helper the arguments name", got)
	}

	var out strings.Builder
	reportFailure(&out, err, commandFor(root, args, executed, err), append([]string{"--json"}, args...))
	if strings.HasPrefix(out.String(), "{") || strings.Contains(out.String(), `"error":`) {
		t.Errorf("a panicking helper reported JSON to its parent:\n%s", out.String())
	}

	// Without a panic the executed command is kept, even when it is nil.
	plain := errors.New("unknown command")
	if got := commandFor(root, []string{"banana"}, nil, plain); got != nil {
		t.Errorf("commandFor without a panic = %v, want nil", got)
	}
	if got := commandFor(root, args, root, plain); got != root {
		t.Errorf("commandFor = %v, want the executed command", got)
	}
}
