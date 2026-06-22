package commands

import (
	"testing"
)

func TestNotifyCmd_HasSubcommands(t *testing.T) {
	cmd := newNotifyCmd()

	expected := map[string]bool{
		"start":    false,
		"stop":     false,
		"status":   false,
		"__daemon": false,
	}
	for _, sub := range cmd.Commands() {
		if _, ok := expected[sub.Name()]; ok {
			expected[sub.Name()] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestNotifyCmd_DaemonIsHidden(t *testing.T) {
	cmd := newNotifyCmd()
	for _, sub := range cmd.Commands() {
		if sub.Name() == "__daemon" && !sub.Hidden {
			t.Error("__daemon subcommand should be hidden")
		}
	}
}

func TestNotifyCmd_RegisteredInRoot(t *testing.T) {
	root := NewRootCmd()
	found := false
	for _, cmd := range root.Commands() {
		if cmd.Name() == "notify" {
			found = true
			break
		}
	}
	if !found {
		t.Error("'notify' command not registered in root")
	}
}
