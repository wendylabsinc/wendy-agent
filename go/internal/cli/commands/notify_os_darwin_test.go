//go:build darwin

package commands

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSendOSNotification_Darwin(t *testing.T) {
	var capturedArgs []string
	origCommand := execCommand
	t.Cleanup(func() { execCommand = origCommand })

	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string{name}, args...)
		return exec.Command("true")
	}

	err := sendOSNotification("Wendy — Warning", "disk is almost full")
	if err != nil {
		t.Fatalf("sendOSNotification: %v", err)
	}

	if len(capturedArgs) == 0 {
		t.Fatal("expected exec.Command to be called")
	}
	if capturedArgs[0] != "osascript" {
		t.Errorf("expected osascript, got %q", capturedArgs[0])
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "Wendy — Warning") {
		t.Errorf("title not in args: %q", joined)
	}
	if !strings.Contains(joined, "disk is almost full") {
		t.Errorf("body not in args: %q", joined)
	}
}
