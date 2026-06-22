//go:build windows

package commands

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

const notifyTaskName = "Wendy Notify"

func notifyLogPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "notify.log"), nil
}

func installNotifyService(cloudGRPC string) error {
	bin := wendyBinaryPath()
	action := fmt.Sprintf(`%s notify __daemon --cloud-grpc %s`, bin, cloudGRPC)

	cmd := execCommand("schtasks",
		"/Create",
		"/TN", notifyTaskName,
		"/TR", action,
		"/SC", "ONLOGON",
		"/F",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func uninstallNotifyService() error {
	cmd := execCommand("schtasks", "/Delete", "/TN", notifyTaskName, "/F")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func notifyServiceStatus() (string, error) {
	cmd := execCommand("schtasks", "/Query", "/TN", notifyTaskName, "/FO", "LIST")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "stopped", nil
	}
	outStr := string(out)
	if strings.Contains(outStr, "Running") {
		return "running", nil
	}
	if strings.Contains(outStr, notifyTaskName) {
		return "installed (not running)", nil
	}
	return "stopped", nil
}
