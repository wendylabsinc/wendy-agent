package commands

import (
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestCompletionGate_ShouldPrompt(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	// A base gate that would prompt; each case flips one field.
	base := completionGate{
		cfg:         &config.Config{},
		now:         now,
		interactive: true,
		jsonOutput:  false,
		firstRun:    false,
		updateShown: false,
		exemptCmd:   false,
	}

	tests := []struct {
		name string
		mut  func(g *completionGate)
		want bool
	}{
		{name: "fresh interactive prompts", mut: func(*completionGate) {}, want: true},
		{name: "non-interactive suppressed", mut: func(g *completionGate) { g.interactive = false }, want: false},
		{name: "json suppressed", mut: func(g *completionGate) { g.jsonOutput = true }, want: false},
		{name: "first run suppressed", mut: func(g *completionGate) { g.firstRun = true }, want: false},
		{name: "update shown suppressed", mut: func(g *completionGate) { g.updateShown = true }, want: false},
		{name: "exempt command suppressed", mut: func(g *completionGate) { g.exemptCmd = true }, want: false},
		{name: "already installed suppressed", mut: func(g *completionGate) { g.cfg.CompletionInstalled = true }, want: false},
		{name: "dismissed suppressed", mut: func(g *completionGate) { g.cfg.CompletionPromptDismissed = true }, want: false},
		{
			name: "throttled within window suppressed",
			mut: func(g *completionGate) {
				g.cfg.LastCompletionPromptCheck = now.Add(-1 * time.Hour).Format(time.RFC3339)
			},
			want: false,
		},
		{
			name: "throttle window elapsed prompts",
			mut: func(g *completionGate) {
				g.cfg.LastCompletionPromptCheck = now.Add(-25 * time.Hour).Format(time.RFC3339)
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := base
			cfgCopy := *base.cfg // don't let cases bleed into each other
			g.cfg = &cfgCopy
			tc.mut(&g)
			if got := g.shouldPrompt(); got != tc.want {
				t.Errorf("shouldPrompt() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestCompletionPromptDue(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		last string
		want bool
	}{
		{name: "never shown", last: "", want: true},
		{name: "unparseable treated as due", last: "not-a-time", want: true},
		{name: "future timestamp treated as due", last: now.Add(time.Hour).Format(time.RFC3339), want: true},
		{name: "recent within window", last: now.Add(-time.Hour).Format(time.RFC3339), want: false},
		{name: "older than window", last: now.Add(-25 * time.Hour).Format(time.RFC3339), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{LastCompletionPromptCheck: tc.last}
			if got := completionPromptDue(cfg, now); got != tc.want {
				t.Errorf("completionPromptDue() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestCompletionExemptCmd(t *testing.T) {
	root := NewRootCmd()

	find := func(path ...string) *cobra.Command {
		cur := root
		for _, name := range path {
			var next *cobra.Command
			for _, c := range cur.Commands() {
				if c.Name() == name {
					next = c
					break
				}
			}
			if next == nil {
				t.Fatalf("command %v not found (stuck at %q)", path, name)
			}
			cur = next
		}
		return cur
	}

	if !completionExemptCmd(find("completion")) {
		t.Error("completion command should be exempt")
	}
	if !completionExemptCmd(find("completion", "install")) {
		t.Error("completion install subcommand should be exempt")
	}
	if !completionExemptCmd(find("tour")) {
		t.Error("tour should be exempt")
	}
	if completionExemptCmd(find("run")) {
		t.Error("run should not be exempt")
	}
}
