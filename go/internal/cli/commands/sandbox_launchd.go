package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const sandboxLaunchAgentLabel = "sh.wendy.sandbox-control-plane"

var (
	// sandboxCommandContext is overridable for testing purposes.
	sandboxCommandContext = exec.CommandContext
)

type sandboxPlistParams struct {
	Label         string
	WorkDir       string
	LogPath       string
	Port          string
	AdminUser     string
	AdminPassword string
	DataDir       string
}

const sandboxPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/env</string>
		<string>node</string>
		<string>dist/index.js</string>
	</array>
	<key>WorkingDirectory</key>
	<string>{{.WorkDir}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PORT</key>
		<string>{{.Port}}</string>
		<key>DRIVER</key>
		<string>docker</string>
		<key>PUBLIC_HOST</key>
		<string>localhost</string>
		<key>ADMIN_USER</key>
		<string>{{.AdminUser}}</string>
		<key>ADMIN_PASSWORD</key>
		<string>{{.AdminPassword}}</string>
		<key>DATA_DIR</key>
		<string>{{.DataDir}}</string>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`

// renderSandboxPlist fills the launchd plist template. Fields are XML-escaped
// before substitution since AdminPassword is base64url in practice but this
// keeps the function correct regardless.
func renderSandboxPlist(p sandboxPlistParams) (string, error) {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	p.Label, p.WorkDir, p.LogPath, p.Port = esc(p.Label), esc(p.WorkDir), esc(p.LogPath), esc(p.Port)
	p.AdminUser, p.AdminPassword, p.DataDir = esc(p.AdminUser), esc(p.AdminPassword), esc(p.DataDir)

	tmpl, err := template.New("sandbox-plist").Parse(sandboxPlistTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func sandboxLaunchctlPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", sandboxLaunchAgentLabel+".plist"), nil
}

func sandboxLaunchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), sandboxLaunchAgentLabel)
}

func loadSandboxLaunchAgent(ctx context.Context, plistPath string) error {
	cmd := sandboxCommandContext(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w", plistPath, err)
	}
	return nil
}

func unloadSandboxLaunchAgent(ctx context.Context) error {
	target := sandboxLaunchdTarget()
	cmd := sandboxCommandContext(ctx, "launchctl", "bootout", target)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl bootout %s: %w", target, err)
	}
	return nil
}

// sandboxLaunchAgentStatus reports whether the LaunchAgent is registered with
// launchd. `launchctl print` exits non-zero when the service isn't loaded —
// that's the normal "not installed" case, not an error to surface.
// Returns (false, nil) if the service is not loaded, (true, nil) if it is loaded,
// and (false, error) if launchctl itself cannot be executed.
func sandboxLaunchAgentStatus(ctx context.Context) (bool, error) {
	target := sandboxLaunchdTarget()
	cmd := sandboxCommandContext(ctx, "launchctl", "print", target)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// launchctl ran but exited non-zero — service not loaded
			return false, nil
		}
		// launchctl itself couldn't run — a real failure
		return false, fmt.Errorf("launchctl print %s: %w", target, err)
	}
	return true, nil
}
