//go:build linux

package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

var notifyUnitTmpl = template.Must(template.New("unit").Parse(`[Unit]
Description=Wendy Cloud Notifications
After=network-online.target

[Service]
ExecStart={{.BinaryPath}} notify __daemon --cloud-grpc {{.CloudGRPC}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`))

func notifyUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", "wendy-notify.service"), nil
}

func notifyLogPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "notify.log"), nil
}

func installNotifyService(cloudGRPC string) error {
	unitPath, err := notifyUnitPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("creating systemd user dir: %w", err)
	}

	f, err := os.Create(unitPath)
	if err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}
	defer f.Close()

	data := struct{ BinaryPath, CloudGRPC string }{
		BinaryPath: wendyBinaryPath(),
		CloudGRPC:  cloudGRPC,
	}
	if err := notifyUnitTmpl.Execute(f, data); err != nil {
		return fmt.Errorf("rendering unit template: %w", err)
	}

	for _, args := range [][]string{
		{"--user", "daemon-reload"},
		{"--user", "enable", "--now", "wendy-notify"},
	} {
		cmd := execCommand("systemctl", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w: %s", args[len(args)-1], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func uninstallNotifyService() error {
	unitPath, err := notifyUnitPath()
	if err != nil {
		return err
	}

	cmd := execCommand("systemctl", "--user", "disable", "--now", "wendy-notify")
	_ = cmd.Run() // ignore: may already be stopped

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func notifyServiceStatus() (string, error) {
	cmd := execCommand("systemctl", "--user", "is-active", "wendy-notify")
	out, err := cmd.Output()
	if err != nil {
		return "stopped", nil
	}
	status := strings.TrimSpace(string(out))
	if status == "active" {
		return "running", nil
	}
	return "stopped", nil
}
