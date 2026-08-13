package commands

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// captureBoth swaps BOTH os.Stdout and os.Stderr with os.Pipe pairs for the
// duration of fn, returning what was written to each. It exists specifically
// to prove cliLogln/cliSuccess write to stderr and NOT stdout — captureStdout
// alone can't distinguish "went to stderr" from "went nowhere".
func captureBoth(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stdout): %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stderr): %v", err)
	}

	oldStdout, oldStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldStdout, oldStderr
	}()

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, outR)
		outCh <- buf.String()
	}()
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, errR)
		errCh <- buf.String()
	}()

	fn()

	_ = outW.Close()
	_ = errW.Close()

	return <-outCh, <-errCh
}

// TestCLILoglnWritesToStderr locks in that cliLogln — a human status line —
// never lands on stdout. --json is a global persistent flag AND auto-enables
// when the terminal is non-interactive, so any status line on stdout can
// corrupt machine-read output (WDY-2435).
func TestCLILoglnWritesToStderr(t *testing.T) {
	stdout, stderr := captureBoth(t, func() {
		cliLogln("hello %s", "world")
	})

	if stdout != "" {
		t.Errorf("cliLogln leaked to stdout: %q", stdout)
	}
	if !bytes.Contains([]byte(stderr), []byte("hello world")) {
		t.Errorf("cliLogln output missing from stderr: %q", stderr)
	}
}

// TestCLISuccessWritesToStderr locks in that cliSuccess — a styled status
// line, not a payload — never lands on stdout, for the same reason as
// TestCLILoglnWritesToStderr above.
func TestCLISuccessWritesToStderr(t *testing.T) {
	stdout, stderr := captureBoth(t, func() {
		cliSuccess("done %s", "here")
	})

	if stdout != "" {
		t.Errorf("cliSuccess leaked to stdout: %q", stdout)
	}
	if !bytes.Contains([]byte(stderr), []byte("done here")) {
		t.Errorf("cliSuccess output missing from stderr: %q", stderr)
	}
}
