package commands

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestRenderSandboxPlist_SubstitutesAllFields(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", NodePath: "/Users/me/.nvm/versions/node/v22.0.0/bin/node",
		WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: "s3cr3t", DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	for _, want := range []string{
		"<string>sh.wendy.sandbox-control-plane</string>",
		"<string>/Users/me/.nvm/versions/node/v22.0.0/bin/node</string>",
		"<string>dist/index.js</string>",
		"<string>/tmp/cp</string>",
		"<string>/tmp/cp.log</string>",
		"<string>8787</string>",
		"<string>admin</string>",
		"<string>s3cr3t</string>",
		"<string>/tmp/cp-data</string>",
		"<key>KeepAlive</key>",
		"<true/>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("rendered plist missing %q\nfull output:\n%s", want, xml)
		}
	}
	// launchd must invoke node by absolute path — a PATH search would miss
	// nvm/fnm/asdf/volta installs entirely.
	if strings.Contains(xml, "/usr/bin/env") {
		t.Errorf("rendered plist still shells out via /usr/bin/env instead of the resolved node path\nfull output:\n%s", xml)
	}
}

func TestRenderSandboxPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", NodePath: `/opt/no&de/bin/node`,
		WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: `a&b<c>d`, DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	if strings.Contains(xml, "a&b<c>d") {
		t.Error("rendered plist contains un-escaped XML special characters in the password")
	}
	if !strings.Contains(xml, "a&amp;b&lt;c&gt;d") {
		t.Errorf("rendered plist missing escaped password\nfull output:\n%s", xml)
	}
	if !strings.Contains(xml, "/opt/no&amp;de/bin/node") {
		t.Errorf("rendered plist missing escaped node path\nfull output:\n%s", xml)
	}
}

func TestSandboxLaunchAgentStatus_ExitErrorIsNotSurfaced(t *testing.T) {
	// Test that when launchctl exits non-zero (e.g., service not loaded),
	// we return (false, nil), not an error. We inject a 'false' command
	// to simulate this scenario.
	oldCommand := sandboxCommandContext
	defer func() { sandboxCommandContext = oldCommand }()

	// Inject a command that exits non-zero (mimics launchctl exiting 1)
	sandboxCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	running, err := sandboxLaunchAgentStatus(context.Background())
	if err != nil {
		t.Errorf("expected (false, nil) but got error: %v", err)
	}
	if running != false {
		t.Errorf("expected running=false, got running=%v", running)
	}
}

func TestSandboxLaunchAgentStatus_RealErrorIsSurfaced(t *testing.T) {
	// Test that when launchctl itself can't run (e.g., not found),
	// we return (false, error), not (false, nil). We inject a command
	// that can't be executed to simulate this scenario.
	oldCommand := sandboxCommandContext
	defer func() { sandboxCommandContext = oldCommand }()

	// Inject a command that can't be found (mimics launchctl not being on PATH)
	sandboxCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("wendy-sandbox-launchd-test-nonexistent-xyz-binary")
	}

	running, err := sandboxLaunchAgentStatus(context.Background())
	if err == nil {
		t.Error("expected error when launchctl can't be executed, got nil")
	}
	if running != false {
		t.Errorf("expected running=false when error occurs, got running=%v", running)
	}
	if !strings.Contains(err.Error(), "launchctl print") {
		t.Errorf("expected error message to mention 'launchctl print', got: %v", err)
	}
}
