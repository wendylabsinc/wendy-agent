package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/wendylabsinc/wendy/go/internal/cli/tui"
)

// appleContainerFormula is the homebrew-core formula that provides Apple's
// `container` CLI.
const appleContainerFormula = "container"

// appleContainerDocsURL points at Apple's container installation docs, shown
// when Homebrew is unavailable to install it automatically.
const appleContainerDocsURL = "https://apple.github.io/container/"

// appleContainerServices are the container services that must be running before
// local build/run, in start order: the system/API service, then the image
// builder.
var appleContainerServices = []string{"system", "builder"}

// appleContainerBrewPaths lists the canonical Homebrew locations, checked in
// order (Apple Silicon first, then Intel). Bypassing $PATH avoids executing an
// unexpected binary in a compromised environment. These paths are root-owned on
// a standard install; world-writable matches are skipped as untrustworthy.
var appleContainerBrewPaths = []string{
	"/opt/homebrew/bin/brew", // Apple Silicon (M-series)
	"/usr/local/bin/brew",    // Intel
}

var (
	appleContainerStat                  = os.Stat
	appleContainerStdout      io.Writer = os.Stdout
	appleContainerStderr      io.Writer = os.Stderr
	appleContainerFindBrew              = defaultAppleContainerFindBrew
	appleContainerInteractive           = func() bool {
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
	appleContainerConfirm = func(question string) (bool, error) {
		return tui.ConfirmDefaultYes(question)
	}
)

// defaultAppleContainerFindBrew returns the first trustworthy brew binary path,
// or "" if none is found.
func defaultAppleContainerFindBrew() string {
	for _, p := range appleContainerBrewPaths {
		info, err := appleContainerStat(p)
		if err != nil {
			continue
		}
		if info.Mode()&0o002 != 0 {
			continue // skip world-writable paths — likely not the legitimate brew binary
		}
		return p
	}
	return ""
}

// ensureAppleContainerInstalled makes sure the `container` CLI is on PATH,
// offering to install it via Homebrew when interactive.
func ensureAppleContainerInstalled(ctx context.Context) error {
	if _, err := appleContainerLookPath("container"); err == nil {
		return nil
	}

	brewPath := appleContainerFindBrew()
	if brewPath == "" {
		return fmt.Errorf("Apple container is not installed; install it via Homebrew (brew install %s) or see %s", appleContainerFormula, appleContainerDocsURL)
	}
	if !appleContainerInteractive() {
		return fmt.Errorf("Apple container is not installed; run: brew install %s", appleContainerFormula)
	}

	confirmed, err := appleContainerConfirm("Apple container is required for local containers. Install it now via Homebrew? (brew install " + appleContainerFormula + ")")
	if err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return fmt.Errorf("Apple container install cancelled")
		}
		return fmt.Errorf("Apple container is not installed (prompt failed: %w); run: brew install %s", err, appleContainerFormula)
	}
	if !confirmed {
		return fmt.Errorf("Apple container is required but not installed; run: brew install %s", appleContainerFormula)
	}

	fmt.Fprintf(appleContainerStdout, "Installing Apple container via Homebrew (brew install %s)...\n", appleContainerFormula)
	cmd := appleContainerCommandContext(ctx, brewPath, "install", appleContainerFormula)
	cmd.Stdout = appleContainerStdout
	cmd.Stderr = appleContainerStderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew install %s: %w", appleContainerFormula, err)
	}

	if _, err := appleContainerLookPath("container"); err != nil {
		return fmt.Errorf("Apple container was installed via Homebrew but is not yet on PATH; open a new terminal or run: eval \"$(brew shellenv)\"")
	}
	return nil
}

// ensureAppleContainerServiceRunning checks `container <service> status` and,
// when the service is down, offers to start it (interactive) or returns an
// actionable error (non-interactive).
func ensureAppleContainerServiceRunning(ctx context.Context, service string) error {
	out, err := appleContainerCommandContext(ctx, "container", service, "status").CombinedOutput()
	if err == nil {
		return nil
	}

	startCmd := fmt.Sprintf("container %s start", service)
	detail := suffixDetail(out)

	if !appleContainerInteractive() {
		return fmt.Errorf("Apple container's %s service is not running%s. Run '%s' and try again", service, detail, startCmd)
	}

	confirmed, cerr := appleContainerConfirm(fmt.Sprintf("Apple container's %s service is not running. Start it now? (%s)", service, startCmd))
	if cerr != nil {
		if errors.Is(cerr, tui.ErrCancelled) {
			return fmt.Errorf("Apple container %s start cancelled", service)
		}
		return fmt.Errorf("Apple container's %s service is not running (prompt failed: %w). Run '%s' and try again", service, cerr, startCmd)
	}
	if !confirmed {
		return fmt.Errorf("Apple container's %s service is required. Run '%s' and try again", service, startCmd)
	}

	fmt.Fprintf(appleContainerStdout, "Starting Apple container %s service (%s)...\n", service, startCmd)
	run := appleContainerCommandContext(ctx, "container", service, "start")
	run.Stdout = appleContainerStdout
	run.Stderr = appleContainerStderr
	if rerr := run.Run(); rerr != nil {
		return fmt.Errorf("%s: %w", startCmd, rerr)
	}
	return nil
}

// suffixDetail formats trimmed command output as a parenthetical suffix, or ""
// when there is nothing to show.
func suffixDetail(out []byte) string {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return ""
	}
	return " (" + msg + ")"
}
