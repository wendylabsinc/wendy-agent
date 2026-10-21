package usermessage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestMappedSentencesContinueAfterColon(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		cause  error
		want   string
	}{
		{"stopping app typo: ", status.Error(codes.NotFound, "private lookup"), "Stopping app typo: the requested item was not found."},
		{"registering deployment with Cloud: ", status.Error(codes.Unavailable, "private outage"), "Registering deployment with Cloud: Wendy Cloud is unavailable. Please try again later."},
		{"listing Wi-Fi networks: ", status.Error(codes.Unimplemented, "Wi-Fi configuration is currently not supported by Wendy Agent for Mac."), "Listing Wi-Fi networks: Wi-Fi configuration is not supported by Wendy Agent for Mac."},
		{"request failed. ", status.Error(codes.NotFound, "private lookup"), "Request failed. The requested item was not found."},
		{"request failed:\n  ", status.Error(codes.NotFound, "private lookup"), "Request failed:\n  The requested item was not found."},
	} {
		for _, got := range []string{Text(fmt.Errorf("%s%w", tc.prefix, tc.cause)), Sprintf(tc.prefix+"%v", tc.cause)} {
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		}
	}
	// A local diagnostic beginning with a proper name retains its spelling.
	if got := Sprintf("build failed: %v", errors.New("Docker could not start")); got != "Build failed: Docker could not start." {
		t.Errorf("authored name changed: %q", got)
	}
}
