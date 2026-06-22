//go:build windows

package commands

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstallNotifyService_Windows(t *testing.T) {
	var schtasksCalls [][]string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if strings.EqualFold(name, "schtasks") || strings.HasSuffix(strings.ToLower(name), "schtasks.exe") {
			schtasksCalls = append(schtasksCalls, append([]string{name}, args...))
			return exec.Command("cmd", "/C", "exit 0")
		}
		return exec.Command(name, args...)
	}

	err := installNotifyService("cloud.example:443")
	if err != nil {
		t.Fatalf("installNotifyService: %v", err)
	}

	if len(schtasksCalls) == 0 {
		t.Fatal("expected schtasks to be called")
	}
	joined := strings.Join(schtasksCalls[0], " ")
	if !strings.Contains(joined, "/Create") {
		t.Errorf("expected /Create in schtasks call: %s", joined)
	}
	if !strings.Contains(joined, "cloud.example:443") {
		t.Errorf("cloud-grpc not in schtasks call: %s", joined)
	}
	if !strings.Contains(joined, "__daemon") {
		t.Errorf("__daemon not in schtasks call: %s", joined)
	}
}

func TestUninstallNotifyService_Windows(t *testing.T) {
	var schtasksCalls [][]string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })
	execCommand = func(name string, args ...string) *exec.Cmd {
		if strings.EqualFold(name, "schtasks") || strings.HasSuffix(strings.ToLower(name), "schtasks.exe") {
			schtasksCalls = append(schtasksCalls, append([]string{name}, args...))
			return exec.Command("cmd", "/C", "exit 0")
		}
		return exec.Command(name, args...)
	}

	if err := uninstallNotifyService(); err != nil {
		t.Fatalf("uninstallNotifyService: %v", err)
	}

	if len(schtasksCalls) == 0 {
		t.Fatal("expected schtasks to be called")
	}
	joined := strings.Join(schtasksCalls[0], " ")
	if !strings.Contains(joined, "/Delete") {
		t.Errorf("expected /Delete in schtasks call: %s", joined)
	}
}
