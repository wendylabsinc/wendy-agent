package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/commands"
)

func TestSyntaxErrorHint(t *testing.T) {
	// Isolate the cache on macOS, Linux, and Windows.
	dir := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CACHE_HOME", "LocalAppData"} {
		t.Setenv(key, dir)
	}
	usage := commands.UnknownSubcommandError([]string{"device", "banana"})
	if usage == nil {
		t.Fatal("expected a syntax error")
	}

	// Each row is the next invocation, sharing the on-disk streak.
	for _, step := range []struct {
		name        string
		err         error
		interactive bool
		json        bool
		hint        bool
	}{
		{"noninteractive first", usage, false, false, false},
		{"noninteractive second", usage, false, false, false},
		{"first", usage, true, false, false},
		{"noninteractive error leaves streak alone", usage, false, false, false},
		{"noninteractive success leaves streak alone", nil, false, false, false},
		{"second", usage, true, false, true},
		{"third", usage, true, false, false},
		{"success resets", nil, true, false, false},
		{"first after success", usage, true, false, false},
		{"second in JSON stays quiet", usage, true, true, false},
		{"third back in text stays quiet", usage, true, false, false},
		{"runtime error resets", errors.New("app start failed"), true, false, false},
		{"first after runtime error", usage, true, false, false},
		{"second after runtime error", usage, true, false, true},
		{"cancellation resets", context.Canceled, true, false, false},
		{"first after cancellation", usage, true, false, false},
		{"second after cancellation", usage, true, false, true},
	} {
		args := []string{"--json=false"}
		if step.json {
			args = []string{"--json"}
		}
		var out strings.Builder
		reportCommandResult(&out, step.err, nil, args, step.interactive)
		if strings.Contains(out.String(), commands.DiscordSupportPrompt) != step.hint {
			t.Fatalf("%s: want hint=%v, got %q", step.name, step.hint, out.String())
		}
		if step.hint && !strings.HasSuffix(out.String(), "\n\n"+commands.DiscordSupportPrompt+"\n") {
			t.Fatalf("%s: hint must follow a blank line: %q", step.name, out.String())
		}
		if step.json {
			decodeEnvelope(t, out.String())
		}
	}
}
