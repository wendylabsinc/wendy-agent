package browseropen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const openCommandTimeout = 10 * time.Second

var (
	goos           = runtime.GOOS
	commandContext = exec.CommandContext
	commandOutput  = commandOutputDefault
	getEnv         = os.Getenv
	getEUID        = os.Geteuid
	glob           = filepath.Glob
	runCommand     = runOpenCommandDefault
	stat           = os.Stat
)

type commandSpec struct {
	name string
	args []string
	env  []string
}

// Open opens url in the platform default browser without waiting for the
// browser process to exit.
func Open(url string) error {
	switch goos {
	case "darwin":
		return runCommand(commandSpec{name: "open", args: []string{url}})
	case "linux":
		return openLinux(url)
	case "windows":
		return runCommand(commandSpec{name: "rundll32", args: []string{"url.dll,FileProtocolHandler", url}})
	default:
		return fmt.Errorf("unsupported platform %q", goos)
	}
}

func openLinux(url string) error {
	if hasCurrentGraphicalSessionEnv() {
		return runCommand(commandSpec{name: "xdg-open", args: []string{url}})
	}

	session, sessionErr := activeGraphicalLoginSession()
	if sessionErr == nil {
		if err := openLinuxInLoginSession(session, url); err == nil {
			return nil
		} else {
			return err
		}
	}

	if err := runCommand(commandSpec{name: "xdg-open", args: []string{url}}); err != nil {
		return fmt.Errorf("xdg-open failed without a graphical session: %w; loginctl: %v", err, sessionErr)
	}
	return nil
}

func hasCurrentGraphicalSessionEnv() bool {
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
		if getEnv(key) != "" {
			return true
		}
	}
	return false
}

type loginSession struct {
	id      string
	uid     string
	user    string
	class   string
	typ     string
	display string
	active  bool
}

func activeGraphicalLoginSession() (loginSession, error) {
	out, err := commandOutput("loginctl", "list-sessions", "--no-legend", "--no-pager")
	if err != nil {
		return loginSession{}, fmt.Errorf("list login sessions: %w", err)
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		session := loginSession{id: fields[0]}
		if len(fields) > 1 {
			session.uid = fields[1]
		}
		if len(fields) > 2 {
			session.user = fields[2]
		}

		props, err := commandOutput(
			"loginctl",
			"show-session",
			session.id,
			"--property=Active",
			"--property=Class",
			"--property=Display",
			"--property=Name",
			"--property=Type",
			"--property=User",
			"--no-pager",
		)
		if err != nil {
			continue
		}
		propsMap := parseLoginctlProperties(string(props))
		if user := propsMap["User"]; user != "" {
			session.uid = user
		}
		if name := propsMap["Name"]; name != "" {
			session.user = name
		}
		session.class = propsMap["Class"]
		session.typ = propsMap["Type"]
		session.display = propsMap["Display"]
		session.active = propsMap["Active"] == "yes"

		if session.isGraphicalUserSession() {
			return session, nil
		}
	}

	return loginSession{}, fmt.Errorf("no active graphical login session found")
}

func parseLoginctlProperties(raw string) map[string]string {
	props := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		props[key] = value
	}
	return props
}

func (s loginSession) isGraphicalUserSession() bool {
	if !s.active {
		return false
	}
	if s.class != "" && s.class != "user" {
		return false
	}
	switch s.typ {
	case "x11", "wayland", "mir":
		return true
	default:
		return s.display != ""
	}
}

func openLinuxInLoginSession(session loginSession, url string) error {
	if session.uid == "" {
		return fmt.Errorf("active graphical login session %q has no uid", session.id)
	}

	env := sessionEnv(session)
	var candidates []commandSpec
	if uid, err := strconv.Atoi(session.uid); err == nil && uid == getEUID() {
		candidates = append(candidates, commandSpec{name: "xdg-open", args: []string{url}, env: env})
	} else if session.user != "" {
		envArgs := append(append([]string{}, env...), "xdg-open", url)
		candidates = append(candidates,
			commandSpec{name: "runuser", args: append([]string{"-u", session.user, "--", "env"}, envArgs...)},
			commandSpec{name: "sudo", args: append([]string{"-n", "-u", session.user, "env"}, envArgs...)},
		)
	} else {
		return fmt.Errorf("active graphical login session %q has no username", session.id)
	}

	var failures []string
	for _, candidate := range candidates {
		if err := runCommand(candidate); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		return nil
	}

	return fmt.Errorf(
		"open browser in graphical login session %s for user %s failed: %s",
		session.id,
		session.user,
		strings.Join(failures, "; "),
	)
}

func sessionEnv(session loginSession) []string {
	runtimeDir := path.Join("/run/user", session.uid)
	env := []string{
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + path.Join(runtimeDir, "bus"),
	}

	if strings.HasPrefix(session.display, ":") {
		env = append(env, "DISPLAY="+session.display)
	} else if session.display != "" {
		env = append(env, "WAYLAND_DISPLAY="+session.display)
	}

	if session.typ == "wayland" && !hasEnv(env, "WAYLAND_DISPLAY") {
		if display := firstWaylandDisplay(runtimeDir); display != "" {
			env = append(env, "WAYLAND_DISPLAY="+display)
		}
	}
	if session.typ == "x11" && !hasEnv(env, "DISPLAY") && fileExists("/tmp/.X11-unix/X0") {
		env = append(env, "DISPLAY=:0")
	}

	return env
}

func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

func firstWaylandDisplay(runtimeDir string) string {
	matches, err := glob(path.Join(runtimeDir, "wayland-*"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return path.Base(matches[0])
}

func fileExists(path string) bool {
	_, err := stat(path)
	return err == nil
}

func runOpenCommandDefault(spec commandSpec) error {
	ctx, cancel := context.WithTimeout(context.Background(), openCommandTimeout)
	defer cancel()

	cmd := commandContext(ctx, spec.name, spec.args...)
	if len(spec.env) > 0 {
		cmd.Env = append(os.Environ(), spec.env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s timed out", spec.name)
		}
		if message != "" {
			return fmt.Errorf("%s failed: %w: %s", spec.name, err, message)
		}
		return fmt.Errorf("%s failed: %w", spec.name, err)
	}
	return nil
}

func commandOutputDefault(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), openCommandTimeout)
	defer cancel()

	out, err := commandContext(ctx, name, args...).Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s timed out", name)
	}
	return out, err
}
