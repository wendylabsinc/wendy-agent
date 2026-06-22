//go:build darwin

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const notifyPlistLabel = "sh.wendy.notify"

var notifyPlistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.BinaryPath}}</string>
    <string>notify</string>
    <string>__daemon</string>
    <string>--cloud-grpc</string>
    <string>{{.CloudGRPC}}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>{{.LogPath}}</string>
  <key>StandardErrorPath</key><string>{{.LogPath}}</string>
</dict>
</plist>
`))

func notifyPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", "sh.wendy.notify.plist"), nil
}

func notifyLogPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "notify.log"), nil
}

func installNotifyService(cloudGRPC string) error {
	plistPath, err := notifyPlistPath()
	if err != nil {
		return err
	}
	logPath, err := notifyLogPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("creating LaunchAgents dir: %w", err)
	}

	f, err := os.Create(plistPath)
	if err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}
	defer f.Close()

	data := struct {
		Label, BinaryPath, CloudGRPC, LogPath string
	}{
		Label:      notifyPlistLabel,
		BinaryPath: wendyBinaryPath(),
		CloudGRPC:  cloudGRPC,
		LogPath:    logPath,
	}
	if err := notifyPlistTmpl.Execute(f, data); err != nil {
		return fmt.Errorf("rendering plist template: %w", err)
	}

	cmd := execCommand("launchctl", "load", plistPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallNotifyService() error {
	plistPath, err := notifyPlistPath()
	if err != nil {
		return err
	}

	cmd := execCommand("launchctl", "unload", plistPath)
	_ = cmd.Run() // ignore: may already be stopped

	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func notifyServiceStatus() (string, error) {
	cmd := execCommand("launchctl", "list", notifyPlistLabel)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "stopped", nil
	}
	if strings.Contains(string(out), notifyPlistLabel) {
		return "running", nil
	}
	return "stopped", nil
}
