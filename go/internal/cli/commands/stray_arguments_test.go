package commands

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Every runnable command that documents no positional argument must reject one,
// rather than accepting and discarding it.
func TestNoCommandSilentlyAcceptsStrayArguments(t *testing.T) {
	var unguarded []string

	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, c := range cmd.Commands() {
			if c.Runnable() && !declaresPositionals(c.Use) {
				if c.Args == nil || c.Args(c, []string{"zzstray"}) == nil {
					unguarded = append(unguarded, c.CommandPath())
				}
			}
			walk(c)
		}
	}
	walk(NewRootCmd())

	if len(unguarded) > 0 {
		t.Errorf("%d command(s) silently accept a stray positional argument:\n  %s",
			len(unguarded), strings.Join(unguarded, "\n  "))
	}
}

// Commands that do document a positional must keep accepting it: the sweep must
// not have blanket-applied NoArgs over a real contract.
func TestCommandsDocumentingPositionalsStillAcceptThem(t *testing.T) {
	for _, path := range [][]string{
		{"device", "logs"},
		{"device", "ros2", "bag", "record"},
		{"os", "update"},
		{"auth", "use"},
		{"json", "validate"},
	} {
		cmd, _, err := NewRootCmd().Find(path)
		if err != nil {
			t.Errorf("Find(%q): %v", path, err)
			continue
		}
		if cmd.Args == nil {
			continue // no validator at all, so nothing is being rejected
		}
		if err := cmd.Args(cmd, []string{"something"}); err != nil {
			t.Errorf("%s rejects its documented positional: %v", cmd.CommandPath(), err)
		}
	}
}

func declaresPositionals(use string) bool {
	fields := strings.Fields(use)
	if len(fields) < 2 {
		return false
	}
	for _, tok := range fields[1:] {
		if tok == "[flags]" {
			continue
		}
		if strings.HasPrefix(tok, "<") || strings.HasPrefix(tok, "[") {
			return true
		}
	}
	return false
}
