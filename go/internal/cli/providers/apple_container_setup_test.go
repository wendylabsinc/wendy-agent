package providers

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// appleContainerSetupStubs configures all seams used by CheckRequirements so a
// test runs fully offline. The returned command log path captures every
// emulated `container`/`brew` invocation.
func appleContainerSetupStubs(t *testing.T) string {
	t.Helper()
	restoreHost := stubAppleContainerHost(t)
	logFile := filepath.Join(t.TempDir(), "commands.log")

	oldCommand := appleContainerCommandContext
	oldLookPath := appleContainerLookPath
	oldFindBrew := appleContainerFindBrew
	oldInteractive := appleContainerInteractive
	oldConfirm := appleContainerConfirm
	oldStdout := appleContainerStdout
	oldStderr := appleContainerStderr
	t.Cleanup(func() {
		restoreHost()
		appleContainerCommandContext = oldCommand
		appleContainerLookPath = oldLookPath
		appleContainerFindBrew = oldFindBrew
		appleContainerInteractive = oldInteractive
		appleContainerConfirm = oldConfirm
		appleContainerStdout = oldStdout
		appleContainerStderr = oldStderr
	})

	appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)
	appleContainerLookPath = func(string) (string, error) { return "/usr/local/bin/container", nil }
	appleContainerFindBrew = func() string { return "/opt/homebrew/bin/brew" }
	appleContainerInteractive = func() bool { return true }
	appleContainerConfirm = func(string) (bool, error) { return true, nil }
	appleContainerStdout = io.Discard
	appleContainerStderr = io.Discard
	return logFile
}

func readAppleContainerLog(t *testing.T, logFile string) string {
	t.Helper()
	data, err := os.ReadFile(logFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ""
		}
		t.Fatal(err)
	}
	return string(data)
}

func TestAppleContainerCheckRequirementsStartsStoppedServices(t *testing.T) {
	logFile := appleContainerSetupStubs(t)
	t.Setenv("APPLE_CONTAINER_HELPER_SYSTEM_DOWN", "1")
	t.Setenv("APPLE_CONTAINER_HELPER_BUILDER_DOWN", "1")

	if err := (&AppleContainerProvider{}).CheckRequirements(context.Background()); err != nil {
		t.Fatalf("CheckRequirements: %v", err)
	}

	log := readAppleContainerLog(t, logFile)
	for _, want := range []string{
		"container\x00system\x00start\n",
		"container\x00builder\x00start\n",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q in:\n%s", want, log)
		}
	}
}

func TestAppleContainerCheckRequirementsServicesRunningDoesNotStart(t *testing.T) {
	logFile := appleContainerSetupStubs(t)

	if err := (&AppleContainerProvider{}).CheckRequirements(context.Background()); err != nil {
		t.Fatalf("CheckRequirements: %v", err)
	}

	log := readAppleContainerLog(t, logFile)
	if strings.Contains(log, "start") {
		t.Fatalf("did not expect any start command in:\n%s", log)
	}
}

func TestAppleContainerCheckRequirementsNonInteractiveStoppedServiceErrors(t *testing.T) {
	appleContainerSetupStubs(t)
	appleContainerInteractive = func() bool { return false }
	t.Setenv("APPLE_CONTAINER_HELPER_SYSTEM_DOWN", "1")

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), "container system start") {
		t.Fatalf("CheckRequirements error = %v, want 'container system start' guidance", err)
	}
}

func TestAppleContainerCheckRequirementsDeclinedServiceErrors(t *testing.T) {
	appleContainerSetupStubs(t)
	appleContainerConfirm = func(string) (bool, error) { return false, nil }
	t.Setenv("APPLE_CONTAINER_HELPER_BUILDER_DOWN", "1")

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), "container builder start") {
		t.Fatalf("CheckRequirements error = %v, want 'container builder start' guidance", err)
	}
}

func TestAppleContainerCheckRequirementsMissingCLIPromptsBrewInstall(t *testing.T) {
	logFile := appleContainerSetupStubs(t)
	calls := 0
	appleContainerLookPath = func(string) (string, error) {
		calls++
		if calls == 1 {
			return "", exec.ErrNotFound
		}
		return "/opt/homebrew/bin/container", nil
	}

	if err := (&AppleContainerProvider{}).CheckRequirements(context.Background()); err != nil {
		t.Fatalf("CheckRequirements: %v", err)
	}

	log := readAppleContainerLog(t, logFile)
	if !strings.Contains(log, "install\x00"+appleContainerFormula+"\n") {
		t.Fatalf("command log missing brew install in:\n%s", log)
	}
}

func TestAppleContainerCheckRequirementsNonInteractiveMissingCLIErrors(t *testing.T) {
	appleContainerSetupStubs(t)
	appleContainerLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	appleContainerInteractive = func() bool { return false }

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), "brew install "+appleContainerFormula) {
		t.Fatalf("CheckRequirements error = %v, want brew install guidance", err)
	}
}

func TestAppleContainerCheckRequirementsMissingCLINoBrewErrors(t *testing.T) {
	appleContainerSetupStubs(t)
	appleContainerLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	appleContainerFindBrew = func() string { return "" }

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), appleContainerDocsURL) {
		t.Fatalf("CheckRequirements error = %v, want docs URL fallback", err)
	}
}

func TestAppleContainerCheckRequirementsBrewInstallFailureSurfaces(t *testing.T) {
	appleContainerSetupStubs(t)
	appleContainerLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Setenv("APPLE_CONTAINER_HELPER_BREW_ERROR", "1")

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), "brew install "+appleContainerFormula) {
		t.Fatalf("CheckRequirements error = %v, want brew install failure", err)
	}
}

type fakeFileInfo struct{ mode fs.FileMode }

func (f fakeFileInfo) Name() string       { return "brew" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestDefaultAppleContainerFindBrewSkipsWorldWritable(t *testing.T) {
	oldStat := appleContainerStat
	oldPaths := appleContainerBrewPaths
	t.Cleanup(func() {
		appleContainerStat = oldStat
		appleContainerBrewPaths = oldPaths
	})
	appleContainerBrewPaths = []string{"/world/writable/brew", "/safe/brew"}
	appleContainerStat = func(p string) (os.FileInfo, error) {
		switch p {
		case "/world/writable/brew":
			return fakeFileInfo{mode: 0o777}, nil
		case "/safe/brew":
			return fakeFileInfo{mode: 0o755}, nil
		}
		return nil, fs.ErrNotExist
	}

	if got := defaultAppleContainerFindBrew(); got != "/safe/brew" {
		t.Fatalf("defaultAppleContainerFindBrew() = %q, want /safe/brew", got)
	}
}
