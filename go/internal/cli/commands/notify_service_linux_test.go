//go:build linux

package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallNotifyService_Linux(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	var systemctlCalls [][]string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "systemctl" {
			systemctlCalls = append(systemctlCalls, append([]string{name}, args...))
			return exec.Command("true")
		}
		return exec.Command(name, args...)
	}

	err := installNotifyService("cloud.example:443")
	if err != nil {
		t.Fatalf("installNotifyService: %v", err)
	}

	home, _ := os.UserHomeDir()
	unitPath := filepath.Join(home, ".config", "systemd", "user", "wendy-notify.service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading unit file: %v", err)
	}
	unit := string(data)
	if !strings.Contains(unit, "__daemon") {
		t.Errorf("unit missing __daemon: %s", unit)
	}
	if !strings.Contains(unit, "cloud.example:443") {
		t.Errorf("unit missing cloud-grpc: %s", unit)
	}

	// Expect daemon-reload then enable --now.
	if len(systemctlCalls) < 2 {
		t.Fatalf("expected 2 systemctl calls, got %d: %v", len(systemctlCalls), systemctlCalls)
	}
	if !strings.Contains(strings.Join(systemctlCalls[0], " "), "daemon-reload") {
		t.Errorf("first call not daemon-reload: %v", systemctlCalls[0])
	}
	if !strings.Contains(strings.Join(systemctlCalls[1], " "), "enable") {
		t.Errorf("second call not enable: %v", systemctlCalls[1])
	}
}

func TestUninstallNotifyService_Linux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	_ = os.MkdirAll(unitDir, 0o755)
	unitPath := filepath.Join(unitDir, "wendy-notify.service")
	_ = os.WriteFile(unitPath, []byte("[Unit]"), 0o644)

	var systemctlCalls [][]string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "systemctl" {
			systemctlCalls = append(systemctlCalls, append([]string{name}, args...))
			return exec.Command("true")
		}
		return exec.Command(name, args...)
	}

	if err := uninstallNotifyService(); err != nil {
		t.Fatalf("uninstallNotifyService: %v", err)
	}

	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Error("unit file should have been removed")
	}
	found := false
	for _, call := range systemctlCalls {
		if strings.Contains(strings.Join(call, " "), "disable") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected systemctl disable, got %v", systemctlCalls)
	}
}
