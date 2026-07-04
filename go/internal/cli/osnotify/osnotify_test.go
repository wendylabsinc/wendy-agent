package osnotify

import (
	"errors"
	"strings"
	"testing"
)

func TestNotifyUsesRunnerWhenToolPresent(t *testing.T) {
	var gotName string
	var gotArgs []string
	origRun, origLook := runner, lookPath
	t.Cleanup(func() { runner, lookPath = origRun, origLook })
	lookPath = func(string) (string, error) { return "/usr/bin/tool", nil }
	runner = func(name string, args ...string) error { gotName = name; gotArgs = args; return nil }

	Notify("T", "B")
	if gotName == "" {
		t.Fatal("expected a notifier command to run")
	}
	joined := gotName + " " + strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "T") && !strings.Contains(joined, "B") {
		t.Errorf("title/body not passed: %q", joined)
	}
}

func TestNotifyNoopWhenToolAbsent(t *testing.T) {
	origRun, origLook := runner, lookPath
	t.Cleanup(func() { runner, lookPath = origRun, origLook })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	called := false
	runner = func(string, ...string) error { called = true; return nil }
	Notify("T", "B") // must not panic; must not run anything
	if called {
		t.Error("runner should not be called when no tool is present")
	}
}
