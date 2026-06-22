//go:build linux

package commands

import (
	"os/exec"
	"strings"
	"testing"
)

func TestSendOSNotification_Linux(t *testing.T) {
	var capturedArgs []string
	origLookPath := execLookPath
	origCommand := execCommand
	t.Cleanup(func() {
		execLookPath = origLookPath
		execCommand = origCommand
	})

	execLookPath = func(file string) (string, error) { return file, nil }
	execCommand = func(name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string{name}, args...)
		return exec.Command("true")
	}

	err := sendOSNotification("Wendy — Error", "device offline")
	if err != nil {
		t.Fatalf("sendOSNotification: %v", err)
	}

	if capturedArgs[0] != "notify-send" {
		t.Errorf("expected notify-send, got %q", capturedArgs[0])
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "Wendy — Error") {
		t.Errorf("title not in args: %q", joined)
	}
	if !strings.Contains(joined, "device offline") {
		t.Errorf("body not in args: %q", joined)
	}
}

func TestSendOSNotification_Linux_MissingNotifySend(t *testing.T) {
	origLookPath := execLookPath
	t.Cleanup(func() { execLookPath = origLookPath })

	execLookPath = func(file string) (string, error) {
		return "", &exec.Error{Name: file, Err: exec.ErrNotFound}
	}

	// Should not error — falls back silently.
	err := sendOSNotification("Wendy", "hello")
	if err != nil {
		t.Fatalf("expected no error when notify-send is absent, got: %v", err)
	}
}
