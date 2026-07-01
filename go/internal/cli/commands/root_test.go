package commands

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommand_HasAllSubcommands(t *testing.T) {
	root := NewRootCmd()

	expectedSubcmds := []string{
		"run",
		"build",
		"init",
		"project",
		"discover",
		"device",
		"os",
		"auth",
		"cache",
		"info",
		"analytics",
		"utils",
	}

	cmds := root.Commands()
	cmdNames := make(map[string]bool)
	for _, c := range cmds {
		cmdNames[c.Name()] = true
	}

	for _, name := range expectedSubcmds {
		if !cmdNames[name] {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestRootCommand_VersionFlag(t *testing.T) {
	root := NewRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetArgs([]string{"--version"})

	err := root.Execute()
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "wendy version") {
		t.Errorf("expected version output, got: %q", output)
	}
}

func TestRootCommand_JSONFlag(t *testing.T) {
	root := NewRootCmd()

	// Verify the flag exists.
	f := root.PersistentFlags().Lookup("json")
	if f == nil {
		t.Fatal("expected --json persistent flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %q; want false", f.DefValue)
	}
}

func TestRootCommand_Help(t *testing.T) {
	root := NewRootCmd()
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetArgs([]string{"--help"})

	err := root.Execute()
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	output := buf.String()
	expectedTexts := []string{
		"Wendy",
		"edge computing",
		"Develop & Deploy",
		"Manage",
		"Cloud",
		"Settings",
		"Flags",
	}
	for _, text := range expectedTexts {
		if !strings.Contains(strings.ToLower(output), strings.ToLower(text)) {
			t.Errorf("help output missing %q", text)
		}
	}

	// Commands that were demoted to hidden must not appear in top-level help,
	// even though they remain registered and runnable.
	for _, hidden := range []string{"build", "watch", "discover", "os", "utils", "info", "mcp", "tour", "auth", "completion"} {
		if strings.Contains(output, "\n  "+hidden+" ") {
			t.Errorf("help output should not list hidden command %q:\n%s", hidden, output)
		}
	}
}

func TestRootCommand_DeviceFlag(t *testing.T) {
	root := NewRootCmd()

	f := root.PersistentFlags().Lookup("device")
	if f == nil {
		t.Fatal("expected --device persistent flag")
	}
}
