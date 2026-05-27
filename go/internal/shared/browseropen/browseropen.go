package browseropen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	openCommandTimeout      = 10 * time.Second
	maxDiagnosticOutputSize = 512
)

type commandSpec struct {
	name string
	args []string
	env  []string
}

type opener struct {
	runtimeGOOS    string
	commandContext func(context.Context, string, ...string) *exec.Cmd
	commandOutput  func(string, ...string) ([]byte, error)
	getenv         func(string) string
	geteuid        func() int
	glob           func(string) ([]string, error)
	runCommand     func(commandSpec) error
}

func newDefaultOpener() opener {
	return opener{
		runtimeGOOS:    runtime.GOOS,
		commandContext: exec.CommandContext,
		getenv:         os.Getenv,
		geteuid:        os.Geteuid,
		glob:           filepath.Glob,
	}
}

// Open opens url in the platform default browser. It waits for the opener
// command to report success or failure, bounded by a short timeout; browser
// processes spawned by the opener are not tracked after that.
func Open(url string) error {
	return newDefaultOpener().open(url)
}

func (o opener) open(url string) error {
	switch o.runtimeGOOS {
	case "darwin":
		return o.run(commandSpec{name: "open", args: []string{url}})
	case "linux":
		return o.openLinux(url)
	case "windows":
		return o.run(commandSpec{name: "rundll32", args: []string{"url.dll,FileProtocolHandler", url}})
	default:
		return fmt.Errorf("unsupported platform %q", o.runtimeGOOS)
	}
}

func (o opener) openLinux(url string) error {
	if o.hasCurrentGraphicalSessionEnv() {
		return o.run(commandSpec{name: "xdg-open", args: []string{url}})
	}

	session, sessionErr := o.activeGraphicalLoginSession()
	if sessionErr == nil {
		if err := o.openLinuxInLoginSession(session, url); err == nil {
			return nil
		} else {
			return err
		}
	}

	if err := o.run(commandSpec{name: "xdg-open", args: []string{url}}); err != nil {
		return fmt.Errorf("xdg-open failed without a graphical session: %w; loginctl: %v", err, sessionErr)
	}
	return nil
}

func (o opener) hasCurrentGraphicalSessionEnv() bool {
	for _, key := range []string{"DISPLAY", "WAYLAND_DISPLAY", "DBUS_SESSION_BUS_ADDRESS"} {
		if o.env(key) != "" {
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

func (o opener) activeGraphicalLoginSession() (loginSession, error) {
	out, err := o.output("loginctl", "list-sessions", "--no-legend", "--no-pager")
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

		props, err := o.output(
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
		return validX11Display(s.display) || validWaylandDisplay(s.display)
	}
}

func (o opener) openLinuxInLoginSession(session loginSession, url string) error {
	uid, err := strconv.Atoi(session.uid)
	if err != nil || uid < 0 {
		return fmt.Errorf("active graphical login session %q has invalid uid", session.id)
	}

	env := o.sessionEnv(session)
	var candidates []commandSpec
	if uid == o.euid() {
		candidates = append(candidates, commandSpec{name: "xdg-open", args: []string{url}, env: env})
	} else if validUsername(session.user) {
		envArgs := append(append([]string{}, env...), "xdg-open", url)
		candidates = append(candidates, commandSpec{name: "runuser", args: append([]string{"-u", session.user, "--", "env"}, envArgs...)})
	} else {
		return fmt.Errorf("active graphical login session %q has invalid username", session.id)
	}

	var failures []string
	for _, candidate := range candidates {
		if err := o.run(candidate); err != nil {
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

func (o opener) sessionEnv(session loginSession) []string {
	runtimeDir := linuxRuntimeDir(session.uid)
	env := []string{
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
	}

	switch {
	case validX11Display(session.display):
		env = append(env, "DISPLAY="+session.display)
	case validWaylandDisplay(session.display):
		env = append(env, "WAYLAND_DISPLAY="+session.display)
	}

	if session.typ == "wayland" && !hasEnv(env, "WAYLAND_DISPLAY") {
		if display := o.firstWaylandDisplay(runtimeDir); display != "" {
			env = append(env, "WAYLAND_DISPLAY="+display)
		}
	}

	return env
}

func linuxRuntimeDir(uid string) string {
	return "/run/user/" + uid
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

func (o opener) firstWaylandDisplay(runtimeDir string) string {
	matches, err := o.globFiles(runtimeDir + "/wayland-*")
	if err != nil || len(matches) == 0 {
		return ""
	}
	display := matches[0]
	if slash := strings.LastIndex(display, "/"); slash >= 0 {
		display = display[slash+1:]
	}
	if !validWaylandDisplay(display) {
		return ""
	}
	return display
}

func validUsername(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if (r >= 'a' && r <= 'z') || r == '_' {
				continue
			}
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validX11Display(value string) bool {
	if !strings.HasPrefix(value, ":") {
		return false
	}
	rest := strings.TrimPrefix(value, ":")
	if rest == "" {
		return false
	}
	parts := strings.Split(rest, ".")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || !allDigits(part) {
			return false
		}
	}
	return true
}

func validWaylandDisplay(value string) bool {
	if !strings.HasPrefix(value, "wayland-") {
		return false
	}
	return allDigits(strings.TrimPrefix(value, "wayland-"))
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (o opener) run(spec commandSpec) error {
	if o.runCommand != nil {
		return o.runCommand(spec)
	}

	ctx, cancel := context.WithTimeout(context.Background(), openCommandTimeout)
	defer cancel()

	cmd := o.command(ctx, spec.name, spec.args...)
	if len(spec.env) > 0 {
		cmd.Env = append(os.Environ(), spec.env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := sanitizeDiagnosticOutput(stderr.String())
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

func (o opener) output(name string, args ...string) ([]byte, error) {
	if o.commandOutput != nil {
		return o.commandOutput(name, args...)
	}

	ctx, cancel := context.WithTimeout(context.Background(), openCommandTimeout)
	defer cancel()

	out, err := o.command(ctx, name, args...).Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s timed out", name)
	}
	return out, err
}

func (o opener) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if o.commandContext != nil {
		return o.commandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func (o opener) env(key string) string {
	if o.getenv != nil {
		return o.getenv(key)
	}
	return os.Getenv(key)
}

func (o opener) euid() int {
	if o.geteuid != nil {
		return o.geteuid()
	}
	return os.Geteuid()
}

func (o opener) globFiles(pattern string) ([]string, error) {
	if o.glob != nil {
		return o.glob(pattern)
	}
	return filepath.Glob(pattern)
}

func sanitizeDiagnosticOutput(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r >= 32 && r <= 126:
			b.WriteRune(r)
		}
		if b.Len() >= maxDiagnosticOutputSize {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
