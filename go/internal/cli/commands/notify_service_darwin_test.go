//go:build darwin

package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallNotifyService_Darwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var launchctlArgs []string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "launchctl" {
			launchctlArgs = append([]string{name}, args...)
			return exec.Command("true")
		}
		return exec.Command(name, args...)
	}

	err := installNotifyService("cloud.example:443")
	if err != nil {
		t.Fatalf("installNotifyService: %v", err)
	}

	// Plist should exist.
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "sh.wendy.notify.plist")
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("reading plist: %v", err)
	}
	plist := string(data)
	if !strings.Contains(plist, "sh.wendy.notify") {
		t.Errorf("plist missing label: %s", plist)
	}
	if !strings.Contains(plist, "cloud.example:443") {
		t.Errorf("plist missing cloud-grpc endpoint: %s", plist)
	}
	if !strings.Contains(plist, "__daemon") {
		t.Errorf("plist missing __daemon subcommand: %s", plist)
	}

	// launchctl load should have been called.
	if len(launchctlArgs) == 0 || launchctlArgs[1] != "load" {
		t.Errorf("expected launchctl load, got %v", launchctlArgs)
	}
}

func TestUninstallNotifyService_Darwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a fake plist.
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(plistDir, "sh.wendy.notify.plist")
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	var launchctlArgs []string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if name == "launchctl" {
			launchctlArgs = append([]string{name}, args...)
			return exec.Command("true")
		}
		return exec.Command(name, args...)
	}

	if err := uninstallNotifyService(); err != nil {
		t.Fatalf("uninstallNotifyService: %v", err)
	}

	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Error("plist should have been removed")
	}
	if len(launchctlArgs) == 0 || launchctlArgs[1] != "unload" {
		t.Errorf("expected launchctl unload, got %v", launchctlArgs)
	}
}
