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
			if c.Runnable() && !c.DisableFlagParsing && !declaresPositionals(c.Use) {
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

// A command that disables Cobra flag parsing owns its complete argv. The
// central stray-argument sweep must not reject that argv before the command's
// parser sees it. This is the sudo re-exec boundary used by Orin recovery.
func TestDisableFlagParsingCommandReceivesItsArguments(t *testing.T) {
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"__t234-write", "--bogus"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `unknown __t234-write flag "--bogus"`) {
		t.Fatalf("Execute() error = %v; want error from __t234-write argument parser", err)
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
