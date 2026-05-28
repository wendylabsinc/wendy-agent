package browseropen

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func testOpener() opener {
	return opener{
		runtimeGOOS: "linux",
		getenv:      func(string) string { return "" },
		geteuid:     func() int { return 0 },
		glob:        func(string) ([]string, error) { return nil, nil },
	}
}

func TestOpenLinuxUsesCurrentGraphicalSessionEnv(t *testing.T) {
	op := testOpener()

	env := map[string]string{"DISPLAY": ":1"}
	op.getenv = func(key string) string { return env[key] }
	op.commandOutput = func(string, ...string) ([]byte, error) {
		t.Fatal("loginctl should not be queried when current process has a display")
		return nil, nil
	}

	var got commandSpec
	op.runCommand = func(spec commandSpec) error {
		got = spec
		return nil
	}

	if err := op.open("https://example.com"); err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	if got.name != xdgOpenPath || !reflect.DeepEqual(got.args, []string{"https://example.com"}) {
		t.Fatalf("command = %#v; want absolute xdg-open URL", got)
	}
	if !got.minimalEnv {
		t.Fatalf("command = %#v; want minimal environment", got)
	}
	if len(got.env) != 0 {
		t.Fatalf("direct xdg-open env = %v; want no overrides", got.env)
	}
}

func TestOpenLinuxBridgesIntoActiveWaylandSession(t *testing.T) {
	op := testOpener()
	op.runuserPaths = []string{"/usr/sbin/runuser"}

	op.commandOutput = func(name string, args ...string) ([]byte, error) {
		if name != loginctlPath {
			t.Fatalf("unexpected command output request: %s %v", name, args)
		}
		if reflect.DeepEqual(args, []string{"list-sessions", "--no-legend", "--no-pager"}) {
			return []byte("2 1000 alice seat0 tty2\n"), nil
		}
		if reflect.DeepEqual(args, []string{
			"show-session",
			"2",
			"--property=Active",
			"--property=Class",
			"--property=Display",
			"--property=Name",
			"--property=Type",
			"--property=User",
			"--no-pager",
		}) {
			return []byte(strings.Join([]string{
				"Active=yes",
				"Class=user",
				"Display=",
				"Name=alice",
				"Type=wayland",
				"User=1000",
				"",
			}, "\n")), nil
		}
		t.Fatalf("unexpected loginctl args: %v", args)
		return nil, nil
	}
	op.glob = func(pattern string) ([]string, error) {
		if pattern != "/run/user/1000/wayland-*" {
			t.Fatalf("glob pattern = %q; want wayland runtime socket pattern", pattern)
		}
		return []string{"/run/user/1000/wayland-1"}, nil
	}

	var got commandSpec
	op.runCommand = func(spec commandSpec) error {
		got = spec
		return nil
	}

	if err := op.open("http://wendy-ser9.local:3001"); err != nil {
		t.Fatalf("Open returned error: %v", err)
	}

	wantArgs := []string{
		"-u", "alice", "--", envPath, "-i",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus",
		"WAYLAND_DISPLAY=wayland-1",
		xdgOpenPath, "http://wendy-ser9.local:3001",
	}
	if got.name != "/usr/sbin/runuser" || !reflect.DeepEqual(got.args, wantArgs) {
		t.Fatalf("command = %#v; want runuser session bridge", got)
	}
	if !got.minimalEnv {
		t.Fatalf("command = %#v; want minimal environment", got)
	}
}

func TestOpenLinuxRejectsInvalidSessionIdentity(t *testing.T) {
	op := testOpener()

	op.commandOutput = func(name string, args ...string) ([]byte, error) {
		if name != loginctlPath {
			t.Fatalf("unexpected command output request: %s %v", name, args)
		}
		switch args[0] {
		case "list-sessions":
			return []byte("2 1000 alice seat0 tty2\n"), nil
		case "show-session":
			return []byte(strings.Join([]string{
				"Active=yes",
				"Class=user",
				"Display=wayland-0",
				"Name=alice;bad",
				"Type=wayland",
				"User=1000",
				"",
			}, "\n")), nil
		default:
			t.Fatalf("unexpected loginctl args: %v", args)
			return nil, nil
		}
	}
	op.runCommand = func(commandSpec) error {
		t.Fatal("runCommand should not be called for an invalid session identity")
		return nil
	}

	err := op.open("https://example.com")
	if err == nil {
		t.Fatal("expected invalid username error")
	}
	if !strings.Contains(err.Error(), "invalid username") {
		t.Fatalf("error = %q; want invalid username diagnostic", err)
	}
}

func TestOpenLinuxReportsMissingGraphicalSession(t *testing.T) {
	op := testOpener()

	op.commandOutput = func(name string, args ...string) ([]byte, error) {
		if name != loginctlPath || args[0] != "list-sessions" {
			t.Fatalf("unexpected command output request: %s %v", name, args)
		}
		return []byte("\n"), nil
	}
	op.runCommand = func(spec commandSpec) error {
		if spec.name != xdgOpenPath {
			t.Fatalf("command = %#v; want xdg-open fallback", spec)
		}
		return errors.New("exit status 3")
	}

	err := op.open("https://example.com")
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

func TestValidateSessionValues(t *testing.T) {
	if !validUsername("alice_1") || !validUsername("Alice.Name") || validUsername("alice;bad") {
		t.Fatal("validUsername did not enforce expected Linux username shape")
	}
	if !validSessionID("session-2") || validSessionID("../../bad") {
		t.Fatal("validSessionID did not enforce expected session id shape")
	}
	if !validX11Display(":0") || !validX11Display(":0.0") || validX11Display(":bad") {
		t.Fatal("validX11Display did not enforce expected display shape")
	}
	if !validWaylandDisplay("wayland-0") || validWaylandDisplay("wayland-main") {
		t.Fatal("validWaylandDisplay did not enforce expected display shape")
	}
	if _, err := validGraphicalSessionUID("1000"); err != nil {
		t.Fatalf("validGraphicalSessionUID returned error for normal uid: %v", err)
	}
	for _, uid := range []string{"0", "1", "999", "-1", "999999"} {
		if _, err := validGraphicalSessionUID(uid); err == nil {
			t.Fatalf("validGraphicalSessionUID(%q) unexpectedly succeeded", uid)
		}
	}
	if err := validateEnvAssignment("DISPLAY=:0"); err != nil {
		t.Fatalf("validateEnvAssignment returned error for DISPLAY: %v", err)
	}
	if err := validateEnvAssignment("DISPLAY=:0\nBAD=1"); err == nil {
		t.Fatal("validateEnvAssignment unexpectedly accepted newline")
	}
	if !validRunuserPath("/usr/sbin/runuser") || validRunuserPath("/tmp/runuser") {
		t.Fatal("validRunuserPath did not enforce expected allow-list")
	}
}

func TestValidateOpenURL(t *testing.T) {
	for _, raw := range []string{"file:///etc/shadow", "javascript:alert(1)", "-https://example.com", "http://", "https://user:pass@example.com"} {
		if err := validateOpenURL(raw); err == nil {
			t.Fatalf("validateOpenURL(%q) unexpectedly succeeded", raw)
		}
	}
	if err := validateOpenURL("https://example.com/path"); err != nil {
		t.Fatalf("validateOpenURL returned error for https URL: %v", err)
	}
}

func TestMinimalCommandEnvOmitsUnrelatedVariables(t *testing.T) {
	op := testOpener()
	env := map[string]string{
		"HOME":      "/home/alice",
		"USER":      "alice",
		"LOGNAME":   "alice",
		"API_TOKEN": "secret",
	}
	op.getenv = func(key string) string { return env[key] }

	got := op.minimalCommandEnv()
	if reflect.DeepEqual(got, []string{}) {
		t.Fatal("minimalCommandEnv returned no environment")
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "API_TOKEN=") {
			t.Fatalf("minimalCommandEnv leaked unrelated variable: %v", got)
		}
	}
}

func TestSanitizeDiagnosticOutput(t *testing.T) {
	got := sanitizeDiagnosticOutput("bad\x1b[31m\nnext\tline\u202e")
	if got != "bad next line?" {
		t.Fatalf("sanitizeDiagnosticOutput = %q; want printable single-line output", got)
	}
}
