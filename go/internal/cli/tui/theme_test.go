package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withColor forces a known color profile so style output is deterministic.
func withColor(t *testing.T, p termenv.Profile) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(p)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

func TestHelpersEmitANSIWhenColorOn(t *testing.T) {
	withColor(t, termenv.TrueColor)
	cases := map[string]func(string) string{
		"Header":  Header,
		"Device":  Device,
		"App":     App,
		"Value":   Value,
		"Command": Command,
		"Path":    Path,
		"Dim":     Dim,
	}
	for name, fn := range cases {
		out := fn("x")
		if !strings.Contains(out, "x") {
			t.Errorf("%s: output %q does not contain input", name, out)
		}
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("%s: expected ANSI escape in %q", name, out)
		}
	}
}

func TestHelpersDegradeWhenColorOff(t *testing.T) {
	withColor(t, termenv.Ascii)
	cases := map[string]func(string) string{
		"Header":  Header,
		"Device":  Device,
		"App":     App,
		"Value":   Value,
		"Command": Command,
		"Path":    Path,
		"Dim":     Dim,
	}
	for name, fn := range cases {
		if out := fn("plain"); out != "plain" {
			t.Errorf("%s: expected bare %q, got %q", name, "plain", out)
		}
	}
}
