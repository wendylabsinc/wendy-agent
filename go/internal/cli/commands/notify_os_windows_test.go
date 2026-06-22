//go:build windows

package commands

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSendOSNotification_Windows(t *testing.T) {
	var capturedArgs []string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })

	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string{name}, args...)
		return exec.Command("cmd", "/C", "exit 0")
	}

	err := sendOSNotification("Wendy — Critical", "system failure")
	if err != nil {
		t.Fatalf("sendOSNotification: %v", err)
	}

	if len(capturedArgs) == 0 {
		t.Fatal("expected exec.Command to be called")
	}
	if !strings.Contains(capturedArgs[0], "powershell") && capturedArgs[0] != "powershell.exe" {
		t.Errorf("expected powershell, got %q", capturedArgs[0])
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "Wendy") {
		t.Errorf("app name not in args: %q", joined)
	}
}
