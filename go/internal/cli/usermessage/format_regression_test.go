package usermessage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// Review 4339052719: the error boundary must never rewrite executable tokens.
func TestSentencePreservesLeadingTokens(t *testing.T) {
	for _, raw := range []string{"--readiness-timeout must be at most 1h", "./wendy.json has an invalid field", `"my-pi" is offline`, "/tmp/wendy.json is invalid"} {
		if got := Text(errors.New(raw)); got != raw+"." {
			t.Errorf("got %q, want %q", got, raw+".")
		}
	}
}

// Review 4339052728: CLI layouts are authored text, including indentation.
func TestFormattingPreservesIndentationAndMultilineText(t *testing.T) {
	for _, tc := range []struct{ format, want string }{
		{"  ✗ %s: %v\n", "  ✗ my-pi: Could not connect.\n"},
		{"  could not kill PID %s: %v\n", "  Could not kill PID my-pi: Could not connect.\n"},
		{"  Error: %v\n    Retry the command\n", "  Error: Could not connect.\n    Retry the command.\n"},
	} {
		args := []any{errors.New("could not connect")}
		if strings.Contains(tc.format, "%s") {
			args = append([]any{"my-pi"}, args...)
		}
		if got := Sprintf(tc.format, args...); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

// Review 4339052798: mapping is pure even when transport debugging is enabled.
func TestFormattingNeverWritesDebugOutput(t *testing.T) {
	t.Setenv("WENDY_TLS_DEBUG", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = previous; r.Close(); w.Close() }()
	cause := Foreign(fmt.Errorf("private transport detail"))
	for range 3 {
		_ = Text(cause)
		_ = Sprintf("%v", cause)
		_ = Server("private agent detail")
	}
	w.Close()
	output, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("rendering wrote diagnostics: %s", output)
	}
}
