package espidftoolchain

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultVersion is the ESP-IDF version used to build Wendy Lite native apps,
// named as eim (the ESP-IDF Installation Manager) names it.
const DefaultVersion = "v5.5.4"

var execCommandContext = exec.CommandContext

// IsEspIdfProject reports whether dir contains an ESP-IDF project.
//
// A directory is considered an ESP-IDF project when it contains an sdkconfig
// (or sdkconfig.defaults) file, or a top-level CMakeLists.txt that includes
// the IDF build system entry point (project.cmake resolved via IDF_PATH or
// an esp-idf directory). A plain CMakeLists.txt without such an include is
// not enough, so generic CMake projects are not misclassified.
func IsEspIdfProject(dir string) bool {
	for _, name := range []string{"sdkconfig", "sdkconfig.defaults"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return true
		}
	}
	content, err := os.ReadFile(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return false
	}
	if !bytes.Contains(content, []byte("project.cmake")) {
		return false
	}
	// Require the include path to reference an IDF install, e.g.
	// include($ENV{IDF_PATH}/tools/cmake/project.cmake) or a path
	// containing an esp-idf directory.
	return bytes.Contains(content, []byte("IDF_PATH")) ||
		bytes.Contains(content, []byte("esp-idf"))
}

// projectNamePattern matches a CMake project() command and captures its first
// argument, the project name. CMake command names are case-insensitive.
var projectNamePattern = regexp.MustCompile(`(?i)^\s*project\s*\(\s*([A-Za-z0-9._-]+)`)

// ProjectName extracts the project name from the top-level CMakeLists.txt in
// dir, i.e. the first argument of the project(...) command. It returns "" if
// the file is missing or contains no project() declaration.
func ProjectName(dir string) string {
	f, err := os.Open(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if m := projectNamePattern.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// ProjectTarget returns the IDF target (SoC name, e.g. "esp32c6") the project
// in dir has been configured for, read from the IDF_TARGET property of
// build/config/sdkconfig.json. It returns "" if the project has not been
// configured yet (file missing, unparseable, or no IDF_TARGET property).
func ProjectTarget(dir string) string {
	content, err := os.ReadFile(filepath.Join(dir, "build", "config", "sdkconfig.json"))
	if err != nil {
		return ""
	}
	var config struct {
		IdfTarget string `json:"IDF_TARGET"`
	}
	if err := json.Unmarshal(content, &config); err != nil {
		return ""
	}
	return config.IdfTarget
}

// EnsureVersion verifies that eim is installed and that the DefaultVersion
// ESP-IDF toolchain is available, installing the toolchain via eim when it
// is missing.
func EnsureVersion(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	checkCmd := execCommandContext(ctx, "eim", "--version")
	checkCmd.Stdout = io.Discard
	checkCmd.Stderr = io.Discard
	if err := checkCmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("eim (ESP-IDF Installation Manager) is not installed; " +
				"install it via 'brew install espressif/eim/eim' or see https://dl.espressif.com/dl/eim/")
		}
		return fmt.Errorf("running 'eim --version': %w", err)
	}

	listCmd := execCommandContext(ctx, "eim", "list")
	out, err := listCmd.Output()
	if err != nil {
		return fmt.Errorf("running 'eim list': %w", err)
	}
	for _, v := range parseInstalledVersions(string(out)) {
		if v == DefaultVersion {
			return nil
		}
	}

	fmt.Fprintf(os.Stdout, "Installing ESP-IDF %s (this may take a while)...\n", DefaultVersion)
	installCmd := execCommandContext(ctx, "eim", "install", "-i", DefaultVersion, "-n", "true")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("installing ESP-IDF %s via eim: %w", DefaultVersion, err)
	}
	return nil
}

// parseInstalledVersions extracts the version names from 'eim list' output,
// which reports installed versions as lines like:
//
//   - v5.5.4 (selected) [/Users/me/.espressif/v5.5.4/esp-idf]
func parseInstalledVersions(output string) []string {
	var versions []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if fields := strings.Fields(line[2:]); len(fields) > 0 {
			versions = append(versions, fields[0])
		}
	}
	return versions
}

// IdfCommandContext returns a command that runs idf.py with the given
// arguments inside the DefaultVersion ESP-IDF environment. 'eim run' performs
// the activation (including the toolchain's Python venv) itself, runs the
// command in the caller's working directory and propagates its exit code.
// eim takes the command as a single string, so arguments must not contain
// spaces.
func IdfCommandContext(ctx context.Context, args ...string) *exec.Cmd {
	command := strings.Join(append([]string{"idf.py"}, args...), " ")
	return execCommandContext(ctx, "eim", "run", command, DefaultVersion)
}
