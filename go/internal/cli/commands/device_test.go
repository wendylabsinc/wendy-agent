package commands

import (
	"testing"
)

func TestDeviceCmd_HasPs(t *testing.T) {
	cmd := newDeviceCmd()
	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Use == "ps" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'ps' subcommand on device command")
	}
}
