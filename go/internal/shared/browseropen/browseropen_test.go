package browseropen

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func withTestHooks(t *testing.T) {
	t.Helper()

	oldGOOS := goos
	oldCommandOutput := commandOutput
	oldGetEnv := getEnv
	oldGetEUID := getEUID
	oldGlob := glob
	oldRunCommand := runCommand
	oldStat := stat

	t.Cleanup(func() {
		goos = oldGOOS
		commandOutput = oldCommandOutput
		getEnv = oldGetEnv
		getEUID = oldGetEUID
		glob = oldGlob
		runCommand = oldRunCommand
		stat = oldStat
	})

	goos = "linux"
	getEnv = func(string) string { return "" }
	getEUID = func() int { return 0 }
	glob = func(string) ([]string, error) { return nil, nil }
	stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
}

func TestOpenLinuxUsesCurrentGraphicalSessionEnv(t *testing.T) {
	withTestHooks(t)

	env := map[string]string{"DISPLAY": ":1"}
	getEnv = func(key string) string { return env[key] }
	commandOutput = func(string, ...string) ([]byte, error) {
		t.Fatal("loginctl should not be queried when current process has a display")
		return nil, nil
	}

	var got commandSpec
	runCommand = func(spec commandSpec) error {
		got = spec
		return nil
	}

	if err := Open("https://example.com"); err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	if got.name != "xdg-open" || !reflect.DeepEqual(got.args, []string{"https://example.com"}) {
		t.Fatalf("command = %#v; want xdg-open URL", got)
	}
	if len(got.env) != 0 {
		t.Fatalf("direct xdg-open env = %v; want no overrides", got.env)
	}
}

func TestOpenLinuxBridgesIntoActiveWaylandSession(t *testing.T) {
	withTestHooks(t)

	commandOutput = func(name string, args ...string) ([]byte, error) {
		if name != "loginctl" {
			t.Fatalf("unexpected command output request: %s %v", name, args)
		}
		switch args[0] {
		case "list-sessions":
			return []byte("2 1000 alice seat0 tty2\n"), nil
		case "show-session":
			return []byte(strings.Join([]string{
				"Active=yes",
				"Class=user",
				"Display=",
				"Name=alice",
				"Type=wayland",
				"User=1000",
				"",
			}, "\n")), nil
		default:
			t.Fatalf("unexpected loginctl args: %v", args)
			return nil, nil
		}
	}
	glob = func(pattern string) ([]string, error) {
		if pattern != "/run/user/1000/wayland-*" {
			t.Fatalf("glob pattern = %q; want wayland runtime socket pattern", pattern)
		}
		return []string{"/run/user/1000/wayland-1"}, nil
	}

	var got commandSpec
	runCommand = func(spec commandSpec) error {
		got = spec
		return nil
	}

	if err := Open("http://wendy-ser9.local:3001"); err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	wantArgs := []string{
		"-u", "alice", "--", "env",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"WAYLAND_DISPLAY=wayland-1",
		"xdg-open", "http://wendy-ser9.local:3001",
	}
	if got.name != "runuser" || !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("command = %#v; want runuser session bridge", got)
	}
}

func TestOpenLinuxReportsMissingGraphicalSession(t *testing.T) {
	withTestHooks(t)

	commandOutput = func(name string, args ...string) ([]byte, error) {
		if name != "loginctl" || args[0] != "list-sessions" {
			t.Fatalf("unexpected command output request: %s %v", name, args)
		}
		return []byte("\n"), nil
	}
	runCommand = func(spec commandSpec) error {
		if spec.name != "xdg-open" {
			t.Fatalf("command = %#v; want xdg-open fallback", spec)
		}
		return errors.New("exit status 3")
	}

	err := Open("https://example.com")
	if err == nil {
		t.Fatal("expected error when xdg-open fails and no graphical session exists")
	}
	if !strings.Contains(err.Error(), "no active graphical login session found") {
		t.Fatalf("error = %q; want missing graphical session diagnostic", err)
	}
}

func TestParseLoginctlProperties(t *testing.T) {
	props := parseLoginctlProperties("Active=yes\nType=x11\nIgnoredLine\nDisplay=:0\n")

	if props["Active"] != "yes" || props["Type"] != "x11" || props["Display"] != ":0" {
		t.Fatalf("props = %#v; want parsed loginctl properties", props)
	}
}
