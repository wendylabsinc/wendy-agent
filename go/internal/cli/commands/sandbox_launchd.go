package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

const sandboxLaunchAgentLabel = "sh.wendy.sandbox-control-plane"

var (
	// sandboxCommandContext is overridable for testing purposes.
	sandboxCommandContext = exec.CommandContext
)

type sandboxPlistParams struct {
	Label string
	// NodePath is the absolute path to the node binary, resolved by the installer
	// via exec.LookPath. launchd runs with a minimal environment, so relying on a
	// PATH search here would break every version-manager install (nvm/fnm/asdf/
	// volta) that isn't under a Homebrew prefix.
	NodePath      string
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
		<string>{{.NodePath}}</string>
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
	p.NodePath = esc(p.NodePath)

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

// sandboxPortIsListening reports whether something accepts TCP connections on
// the given local port. Being loaded in launchd is not the same as being alive:
// with KeepAlive: true a crash-looping control-plane stays registered forever,
// so this is the only signal that distinguishes "running" from "respawning".
func sandboxPortIsListening(ctx context.Context, port string) bool {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "tcp", "localhost:"+port)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
