package commands

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/providers"
)

func mustDetectProjectType(t *testing.T, dir string) string {
	t.Helper()
	got, err := detectProjectType(dir)
	if err != nil {
		t.Fatalf("detectProjectType unexpected error: %v", err)
	}
	return got
}

func TestBuildAndPushImageWithAppleContainerUsesContainerCLI(t *testing.T) {
	oldCommand := imageBuilderCommandContext
	oldLookPath := imageBuilderLookPath
	oldGOOS := imageBuilderHostGOOS
	oldGOARCH := imageBuilderHostGOARCH
	t.Cleanup(func() {
		imageBuilderCommandContext = oldCommand
		imageBuilderLookPath = oldLookPath
		imageBuilderHostGOOS = oldGOOS
		imageBuilderHostGOARCH = oldGOARCH
	})

	logFile := filepath.Join(t.TempDir(), "commands.log")
	imageBuilderCommandContext = fakeImageBuilderCommandContext(logFile)
	imageBuilderLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}
	imageBuilderHostGOOS = func() string { return "darwin" }
	imageBuilderHostGOARCH = func() string { return "arm64" }

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY app.py .\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := buildAndPushImageWithBuilder(
		context.Background(),
		imageBuilderAppleContainer,
		dir,
		"127.0.0.1:5000",
		"127.0.0.1:5000/test-app:latest",
		"linux/arm64",
		"Dockerfile",
		map[string]string{"B": "2", "A": "1"},
		"",
		io.Discard,
		io.Discard,
		false,
	)
	if err != nil {
		t.Fatalf("buildAndPushImageWithBuilder: %v", err)
	}

	resolvedDockerfile, err := confinedDockerfilePath(dir, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	buildContext, err := appleContainerBuildContextPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{
		"container\x00--version\n",
		"container\x00system\x00status\n",
		"container\x00build\x00--progress\x00plain\x00--platform\x00linux/arm64\x00-t\x00127.0.0.1:5000/test-app:latest\x00-f\x00" + resolvedDockerfile + "\x00--build-arg\x00A=1\x00--build-arg\x00B=2\x00" + buildContext + "\n",
		"container\x00image\x00push\x00--scheme\x00http\x00--platform\x00linux/arm64\x00127.0.0.1:5000/test-app:latest\n",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q in:\n%s", want, log)
		}
	}
}

func TestBuildImageToOCILayoutWithAppleContainer(t *testing.T) {
	oldCommand := imageBuilderCommandContext
	t.Cleanup(func() { imageBuilderCommandContext = oldCommand })
	logFile := filepath.Join(t.TempDir(), "commands.log")
	imageBuilderCommandContext = fakeImageBuilderCommandContext(logFile)

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "wendy-oci-123", "image.tar")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}

	// Route through the apple-container branch of the fast OCI-layout build.
	err := buildImageToOCILayout(context.Background(), cwd, "Dockerfile", "linux/arm64",
		map[string]string{"A": "1"}, imageBuilderAppleContainer, dest, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("buildImageToOCILayout(apple-container): %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	// Builds into the image store under a unique tag, exports via image save,
	// then removes the temporary tag.
	for _, want := range []string{
		"container\x00build\x00--progress\x00plain\x00--platform\x00linux/arm64\x00-t\x00wendy-oci-build:wendy-oci-123",
		"container\x00image\x00save\x00wendy-oci-build:wendy-oci-123\x00--platform\x00linux/arm64\x00-o\x00" + dest,
		"container\x00image\x00rm\x00wendy-oci-build:wendy-oci-123",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q in:\n%s", want, log)
		}
	}
}

func TestRedactBuildArgsForLog(t *testing.T) {
	in := []string{
		"build", "--platform", "linux/arm64", "-t", "img:latest",
		"--build-arg", "API_TOKEN=s3cr3t",
		"--build-arg", "WENDY_DEBUG=false",
		".",
	}
	got := strings.Join(redactBuildArgsForLog(in), " ")
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("redacted command still contains secret value: %s", got)
	}
	for _, want := range []string{"API_TOKEN=<redacted>", "WENDY_DEBUG=<redacted>", "-t img:latest"} {
		if !strings.Contains(got, want) {
			t.Fatalf("redacted command missing %q: %s", want, got)
		}
	}
	// The input slice must not be mutated (it is used to run the real command).
	if in[6] != "API_TOKEN=s3cr3t" {
		t.Fatalf("redactBuildArgsForLog mutated its input: %q", in[6])
	}
}

func TestAppleContainerPushSchemeRequiresLoopbackRegistry(t *testing.T) {
	for _, image := range []string{
		"127.0.0.1:5000/test-app:latest",
		"127.42.0.1:5000/test-app:latest",
		"localhost:5000/test-app:latest",
		"[::1]:5000/test-app:latest",
		"[::ffff:127.0.0.1]:5000/test-app:latest",
		"[::ffff:7f00:1]:5000/test-app:latest",
	} {
		scheme, err := appleContainerPushScheme(image)
		if err != nil {
			t.Fatalf("appleContainerPushScheme(%q): %v", image, err)
		}
		if scheme != "http" {
			t.Fatalf("appleContainerPushScheme(%q) = %q, want http", image, scheme)
		}
	}

	for _, image := range []string{
		"192.168.1.20:5000/test-app:latest",
		"[::ffff:192.168.1.20]:5000/test-app:latest",
		"0.0.0.0:5000/test-app:latest",
		"[::]:5000/test-app:latest",
		"my-wendy.local:5000/test-app:latest",
		"host.docker.internal:5000/test-app:latest",
	} {
		if _, err := appleContainerPushScheme(image); err == nil {
			t.Fatalf("appleContainerPushScheme(%q) = nil, want error", image)
		}
	}
}

func TestLogAppleContainerFallbackOmitsRawError(t *testing.T) {
	var buf bytes.Buffer
	logAppleContainerFallback(&buf, errors.New("secret path /tmp/wendy/project"))
	got := buf.String()
	if !strings.Contains(got, "[WARN] Apple Container unavailable or failed; falling back to Docker") {
		t.Fatalf("fallback log = %q, want warning", got)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "/tmp/wendy/project") {
		t.Fatalf("fallback log leaked raw error details: %q", got)
	}
}

func TestBuildDockerProjectWithBuilderDefaultsToAppleContainerOnAppleSilicon(t *testing.T) {
	oldCommand := imageBuilderCommandContext
	oldLookPath := imageBuilderLookPath
	oldGOOS := imageBuilderHostGOOS
	oldGOARCH := imageBuilderHostGOARCH
	oldDockerBuild := buildDockerProjectWithDocker
	t.Cleanup(func() {
		imageBuilderCommandContext = oldCommand
		imageBuilderLookPath = oldLookPath
		imageBuilderHostGOOS = oldGOOS
		imageBuilderHostGOARCH = oldGOARCH
		buildDockerProjectWithDocker = oldDockerBuild
	})

	logFile := filepath.Join(t.TempDir(), "commands.log")
	imageBuilderCommandContext = fakeImageBuilderCommandContext(logFile)
	imageBuilderLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}
	imageBuilderHostGOOS = func() string { return "darwin" }
	imageBuilderHostGOARCH = func() string { return "arm64" }
	buildDockerProjectWithDocker = func(dir, imageName, platform, dockerfile string) error {
		t.Fatal("Docker fallback should not run when Apple Container succeeds")
		return nil
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := buildDockerProjectWithBuilder(context.Background(), "", dir, "test-app:latest", "linux/arm64", "Containerfile"); err != nil {
		t.Fatalf("buildDockerProjectWithBuilder: %v", err)
	}

	resolvedBuildFile, err := confinedDockerfilePath(dir, "Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	buildContext, err := appleContainerBuildContextPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "container\x00build\x00--progress\x00plain\x00--platform\x00linux/arm64\x00-t\x00test-app:latest\x00-f\x00" + resolvedBuildFile + "\x00" + buildContext + "\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("command log missing %q in:\n%s", want, string(data))
	}
}

func TestBuildDockerProjectWithBuilderFallsBackToDockerWhenAutoAppleContainerFails(t *testing.T) {
	oldCommand := imageBuilderCommandContext
	oldLookPath := imageBuilderLookPath
	oldGOOS := imageBuilderHostGOOS
	oldGOARCH := imageBuilderHostGOARCH
	oldDockerBuild := buildDockerProjectWithDocker
	t.Cleanup(func() {
		imageBuilderCommandContext = oldCommand
		imageBuilderLookPath = oldLookPath
		imageBuilderHostGOOS = oldGOOS
		imageBuilderHostGOARCH = oldGOARCH
		buildDockerProjectWithDocker = oldDockerBuild
	})

	logFile := filepath.Join(t.TempDir(), "commands.log")
	t.Setenv("IMAGE_BUILDER_FAIL_CONTAINER_BUILD", "1")
	imageBuilderCommandContext = fakeImageBuilderCommandContext(logFile)
	imageBuilderLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}
	imageBuilderHostGOOS = func() string { return "darwin" }
	imageBuilderHostGOARCH = func() string { return "arm64" }

	var dockerFallbackCalled bool
	buildDockerProjectWithDocker = func(dir, imageName, platform, dockerfile string) error {
		dockerFallbackCalled = true
		if imageName != "test-app:latest" || platform != "linux/arm64" || dockerfile != "Dockerfile" {
			t.Fatalf("fallback args = (%q, %q, %q), want image/platform/Dockerfile", imageName, platform, dockerfile)
		}
		return nil
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := buildDockerProjectWithBuilder(context.Background(), "", dir, "test-app:latest", "linux/arm64", "Dockerfile"); err != nil {
		t.Fatalf("buildDockerProjectWithBuilder: %v", err)
	}
	if !dockerFallbackCalled {
		t.Fatal("Docker fallback was not called")
	}
}

// When Apple Container's CLI is installed but its system is not running, the
// no-builder auto path now starts the system and uses Apple Container instead
// of silently falling back to Docker.
func TestBuildDockerProjectWithBuilderStartsAppleContainerWhenStoppedOnAppleSilicon(t *testing.T) {
	logFile := setupAppleContainerEnsureSeams(t)
	isInteractiveTerminalFn = func() bool { return false }
	promptYesNoFn = func(string) bool { t.Fatal("must not prompt in non-interactive auto path"); return false }
	// System is down until "container system start" creates the readiness file.
	t.Setenv("IMAGE_BUILDER_STATUS_READY_FILE", filepath.Join(t.TempDir(), "ready"))

	oldDockerBuild := buildDockerProjectWithDocker
	t.Cleanup(func() { buildDockerProjectWithDocker = oldDockerBuild })
	buildDockerProjectWithDocker = func(dir, imageName, platform, dockerfile string) error {
		t.Fatal("Docker fallback should not run when Apple Container can be started")
		return nil
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := buildDockerProjectWithBuilder(context.Background(), "", dir, "test-app:latest", "linux/arm64", "Containerfile"); err != nil {
		t.Fatalf("buildDockerProjectWithBuilder: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "container\x00system\x00start\x00--timeout\x0060") {
		t.Fatalf("expected Apple Container system to be started:\n%s", data)
	}
	if !strings.Contains(string(data), "container\x00build\x00") {
		t.Fatalf("expected Apple Container build to run:\n%s", data)
	}
}

func fakeImageBuilderCommandContext(logFile string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestImageBuilderHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_IMAGE_BUILDER_HELPER_PROCESS=1",
			"IMAGE_BUILDER_HELPER_LOG="+logFile,
		)
		return cmd
	}
}

func TestImageBuilderHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_IMAGE_BUILDER_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if logFile := os.Getenv("IMAGE_BUILDER_HELPER_LOG"); logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.WriteString(strings.Join(args, "\x00") + "\n")
			_ = f.Close()
		}
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "--version" {
		_, _ = os.Stdout.WriteString("container 1.0.0\n")
		os.Exit(0)
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "system" && args[2] == "start" {
		// When a readiness-file is configured, "start" makes the system ready by
		// creating it (unless told to fail). This models the real start/poll loop.
		if os.Getenv("IMAGE_BUILDER_FAIL_START") == "1" {
			_, _ = os.Stderr.WriteString("failed to decode apiServerBuild in health check\n")
			os.Exit(1)
		}
		if rf := os.Getenv("IMAGE_BUILDER_STATUS_READY_FILE"); rf != "" {
			_ = os.WriteFile(rf, []byte("ready"), 0o644)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "system" && args[2] == "status" {
		// A readiness-file gate models a system that is down until "start" runs.
		if rf := os.Getenv("IMAGE_BUILDER_STATUS_READY_FILE"); rf != "" {
			if _, err := os.Stat(rf); err != nil {
				_, _ = os.Stderr.WriteString("apiserver is not running\n")
				os.Exit(1)
			}
			os.Exit(0)
		}
		if os.Getenv("IMAGE_BUILDER_FAIL_STATUS") == "1" {
			_, _ = os.Stderr.WriteString("apiserver is not running\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "build" {
		if os.Getenv("IMAGE_BUILDER_FAIL_CONTAINER_BUILD") == "1" {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "image" && args[2] == "push" {
		os.Exit(0)
	}
	if len(args) >= 5 && args[0] == "container" && args[1] == "image" && args[2] == "save" {
		// Emit a placeholder file at the -o dest so the host-side file exists.
		for i := 3; i < len(args)-1; i++ {
			if args[i] == "-o" {
				_ = os.WriteFile(args[i+1], []byte("oci-tar"), 0o644)
			}
		}
		os.Exit(0)
	}
	if len(args) >= 4 && args[0] == "container" && args[1] == "image" && args[2] == "rm" {
		os.Exit(0)
	}
	os.Exit(1)
}

// setupAppleContainerEnsureSeams installs the fakes ensureAppleContainerSystem
// depends on (Apple silicon host, container CLI on PATH, fast timeouts) and
// returns the path to the recorded command log.
func setupAppleContainerEnsureSeams(t *testing.T) string {
	t.Helper()
	oldCommand := imageBuilderCommandContext
	oldLookPath := imageBuilderLookPath
	oldGOOS := imageBuilderHostGOOS
	oldGOARCH := imageBuilderHostGOARCH
	oldInteractive := isInteractiveTerminalFn
	oldPrompt := promptYesNoFn
	oldTimeout := appleContainerStartTimeout
	oldPoll := appleContainerStatusPollInterval
	t.Cleanup(func() {
		imageBuilderCommandContext = oldCommand
		imageBuilderLookPath = oldLookPath
		imageBuilderHostGOOS = oldGOOS
		imageBuilderHostGOARCH = oldGOARCH
		isInteractiveTerminalFn = oldInteractive
		promptYesNoFn = oldPrompt
		appleContainerStartTimeout = oldTimeout
		appleContainerStatusPollInterval = oldPoll
	})

	logFile := filepath.Join(t.TempDir(), "commands.log")
	imageBuilderCommandContext = fakeImageBuilderCommandContext(logFile)
	imageBuilderLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}
	imageBuilderHostGOOS = func() string { return "darwin" }
	imageBuilderHostGOARCH = func() string { return "arm64" }
	appleContainerStartTimeout = 300 * time.Millisecond
	appleContainerStatusPollInterval = 20 * time.Millisecond
	return logFile
}

func TestEnsureAppleContainerSystem_AlreadyRunning(t *testing.T) {
	logFile := setupAppleContainerEnsureSeams(t)
	isInteractiveTerminalFn = func() bool { return false }

	if err := ensureAppleContainerSystem(context.Background(), false); err != nil {
		t.Fatalf("ensureAppleContainerSystem: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	if strings.Contains(string(data), "system\x00start") {
		t.Fatalf("did not expect 'system start' when already running:\n%s", data)
	}
}

func TestEnsureAppleContainerSystem_AutoStartsWhenAssumeYes(t *testing.T) {
	logFile := setupAppleContainerEnsureSeams(t)
	isInteractiveTerminalFn = func() bool { return false }
	promptYesNoFn = func(string) bool { t.Fatal("must not prompt when assumeYes"); return false }
	// System is down until "container system start" creates the readiness file.
	t.Setenv("IMAGE_BUILDER_STATUS_READY_FILE", filepath.Join(t.TempDir(), "ready"))

	if err := ensureAppleContainerSystem(context.Background(), true); err != nil {
		t.Fatalf("ensureAppleContainerSystem: %v", err)
	}

	data, _ := os.ReadFile(logFile)
	if !strings.Contains(string(data), "container\x00system\x00start\x00--timeout\x0060") {
		t.Fatalf("expected 'container system start --timeout 60' to be invoked:\n%s", data)
	}
}

func TestEnsureAppleContainerSystem_InteractiveDeclined(t *testing.T) {
	logFile := setupAppleContainerEnsureSeams(t)
	t.Setenv("IMAGE_BUILDER_FAIL_STATUS", "1")
	isInteractiveTerminalFn = func() bool { return true }
	promptYesNoFn = func(string) bool { return false }

	err := ensureAppleContainerSystem(context.Background(), false)
	if err == nil {
		t.Fatal("expected error when the user declines to start the system")
	}

	data, _ := os.ReadFile(logFile)
	if strings.Contains(string(data), "system\x00start") {
		t.Fatalf("did not expect 'system start' after the user declined:\n%s", data)
	}
}

func TestEnsureAppleContainerSystem_StartFailsSurfacesOutput(t *testing.T) {
	setupAppleContainerEnsureSeams(t)
	t.Setenv("IMAGE_BUILDER_FAIL_STATUS", "1")
	t.Setenv("IMAGE_BUILDER_FAIL_START", "1")
	isInteractiveTerminalFn = func() bool { return false }

	err := ensureAppleContainerSystem(context.Background(), true)
	if err == nil {
		t.Fatal("expected error when the system cannot start")
	}
	if !strings.Contains(err.Error(), "could not start Apple Container system") {
		t.Fatalf("error = %v, want 'could not start Apple Container system'", err)
	}
	if !strings.Contains(err.Error(), "failed to decode apiServerBuild in health check") {
		t.Fatalf("error = %v, want the start output summary surfaced", err)
	}
}

func TestWatchCommandHasBuilderFlag(t *testing.T) {
	if f := newWatchCmd().Flags().Lookup("builder"); f == nil {
		t.Fatal("wendy watch is missing the --builder flag")
	}
}

func TestEnsureDockerDaemon_DarwinUsesBundledCLIWhenRuntimeInstalledButDockerMissingFromPath(t *testing.T) {
	oldRuntimes := darwinDockerRuntimes
	oldLookPath := dockerLookPathFn
	oldVersionOK := dockerVersionOKFn
	oldOpenRuntime := dockerOpenRuntimeFn
	oldInstallRuntime := dockerInstallRuntimeFn
	oldInteractive := isInteractiveTerminalFn
	t.Cleanup(func() {
		darwinDockerRuntimes = oldRuntimes
		dockerLookPathFn = oldLookPath
		dockerVersionOKFn = oldVersionOK
		dockerOpenRuntimeFn = oldOpenRuntime
		dockerInstallRuntimeFn = oldInstallRuntime
		isInteractiveTerminalFn = oldInteractive
	})

	dir := t.TempDir()
	appPath := filepath.Join(dir, "Docker.app")
	cliPath := filepath.Join(appPath, "Contents", "Resources", "bin", "docker")
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	darwinDockerRuntimes = []dockerRuntime{{
		name:        "Docker Desktop",
		app:         appPath,
		cliPaths:    []string{cliPath},
		cliLinkHint: "link Docker Desktop CLI tools",
	}}
	t.Setenv("PATH", filepath.Join(dir, "no-docker-on-path"))
	cliDir := filepath.Dir(cliPath)

	dockerLookPathFn = func(file string) (string, error) {
		if file == "docker" && pathHasDir(os.Getenv("PATH"), cliDir) {
			return cliPath, nil
		}
		return "", errors.New("not found")
	}
	versionChecks := 0
	dockerVersionOKFn = func(context.Context) bool {
		versionChecks++
		return pathHasDir(os.Getenv("PATH"), cliDir)
	}
	dockerOpenRuntimeFn = func(context.Context, string) error {
		t.Fatal("should not open Docker Desktop once bundled CLI works")
		return nil
	}
	dockerInstallRuntimeFn = func(context.Context) error {
		t.Fatal("should not prompt/install Docker Desktop when the app is already installed")
		return nil
	}
	isInteractiveTerminalFn = func() bool { return false }

	if err := ensureDockerDaemonForHostOS(context.Background(), dockerHostOSDarwin); err != nil {
		t.Fatalf("ensureDockerDaemonForHostOS: %v", err)
	}
	if !pathHasDir(os.Getenv("PATH"), cliDir) {
		t.Fatalf("PATH = %q, want bundled CLI dir %q", os.Getenv("PATH"), cliDir)
	}
	if versionChecks < 2 {
		t.Fatalf("version checks = %d, want initial failure and retry after PATH update", versionChecks)
	}
}

func TestEnsureDockerDaemon_DarwinRuntimeInstalledButDockerCLIMissingDiagnostic(t *testing.T) {
	oldRuntimes := darwinDockerRuntimes
	oldLookPath := dockerLookPathFn
	oldVersionOK := dockerVersionOKFn
	oldOpenRuntime := dockerOpenRuntimeFn
	oldInstallRuntime := dockerInstallRuntimeFn
	oldInteractive := isInteractiveTerminalFn
	t.Cleanup(func() {
		darwinDockerRuntimes = oldRuntimes
		dockerLookPathFn = oldLookPath
		dockerVersionOKFn = oldVersionOK
		dockerOpenRuntimeFn = oldOpenRuntime
		dockerInstallRuntimeFn = oldInstallRuntime
		isInteractiveTerminalFn = oldInteractive
	})

	dir := t.TempDir()
	appPath := filepath.Join(dir, "Docker.app")
	if err := os.MkdirAll(appPath, 0o755); err != nil {
		t.Fatal(err)
	}

	darwinDockerRuntimes = []dockerRuntime{{
		name:        "Docker Desktop",
		app:         appPath,
		cliPaths:    []string{filepath.Join(appPath, "Contents", "Resources", "bin", "docker")},
		cliLinkHint: "link Docker Desktop CLI tools",
	}}
	t.Setenv("PATH", filepath.Join(dir, "no-docker-on-path"))
	dockerLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	dockerVersionOKFn = func(context.Context) bool { return false }
	dockerOpenRuntimeFn = func(context.Context, string) error {
		t.Fatal("should not open runtime when no docker CLI is available")
		return nil
	}
	dockerInstallRuntimeFn = func(context.Context) error {
		t.Fatal("should not prompt/install Docker Desktop when the app is already installed")
		return nil
	}
	isInteractiveTerminalFn = func() bool { return false }

	err := ensureDockerDaemonForHostOS(context.Background(), dockerHostOSDarwin)
	if err == nil {
		t.Fatal("expected docker CLI missing diagnostic")
	}
	msg := err.Error()
	for _, want := range []string{"Docker Desktop is installed", "docker CLI is not on PATH", "link Docker Desktop CLI tools"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

func TestEnsureDockerDaemon_WindowsUsesBundledCLIWhenDockerDesktopInstalledButDockerMissingFromPath(t *testing.T) {
	oldWindowsRuntimes := windowsDockerRuntimes
	oldLookPath := dockerLookPathFn
	oldVersionOK := dockerVersionOKFn
	t.Cleanup(func() {
		windowsDockerRuntimes = oldWindowsRuntimes
		dockerLookPathFn = oldLookPath
		dockerVersionOKFn = oldVersionOK
	})

	dir := t.TempDir()
	appPath := filepath.Join(dir, "Docker Desktop.exe")
	cliPath := filepath.Join(dir, "resources", "bin", "docker.exe")
	if err := os.MkdirAll(filepath.Dir(cliPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appPath, []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cliPath, []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}

	windowsDockerRuntimes = []dockerRuntime{{
		name:        "Docker Desktop",
		app:         appPath,
		cliPaths:    []string{cliPath},
		cliLinkHint: "add Docker Desktop CLI tools to PATH",
	}}
	t.Setenv("PATH", filepath.Join(dir, "no-docker-on-path"))
	cliDir := filepath.Dir(cliPath)

	dockerLookPathFn = func(file string) (string, error) {
		if file == "docker" && pathHasDir(os.Getenv("PATH"), cliDir) {
			return cliPath, nil
		}
		return "", errors.New("not found")
	}
	versionChecks := 0
	dockerVersionOKFn = func(context.Context) bool {
		versionChecks++
		return pathHasDir(os.Getenv("PATH"), cliDir)
	}

	if err := ensureDockerDaemonForHostOS(context.Background(), dockerHostOSWindows); err != nil {
		t.Fatalf("ensureDockerDaemonForHostOS: %v", err)
	}
	if !pathHasDir(os.Getenv("PATH"), cliDir) {
		t.Fatalf("PATH = %q, want bundled CLI dir %q", os.Getenv("PATH"), cliDir)
	}
	if versionChecks < 2 {
		t.Fatalf("version checks = %d, want initial failure and retry after PATH update", versionChecks)
	}
}

func TestEnsureDockerDaemon_WindowsRuntimeInstalledButDockerCLIMissingDiagnostic(t *testing.T) {
	oldWindowsRuntimes := windowsDockerRuntimes
	oldLookPath := dockerLookPathFn
	oldVersionOK := dockerVersionOKFn
	t.Cleanup(func() {
		windowsDockerRuntimes = oldWindowsRuntimes
		dockerLookPathFn = oldLookPath
		dockerVersionOKFn = oldVersionOK
	})

	dir := t.TempDir()
	appPath := filepath.Join(dir, "Docker Desktop.exe")
	if err := os.WriteFile(appPath, []byte("exe"), 0o755); err != nil {
		t.Fatal(err)
	}

	windowsDockerRuntimes = []dockerRuntime{{
		name:        "Docker Desktop",
		app:         appPath,
		cliPaths:    []string{filepath.Join(dir, "resources", "bin", "docker.exe")},
		cliLinkHint: "repair Docker Desktop CLI tools",
	}}
	t.Setenv("PATH", filepath.Join(dir, "no-docker-on-path"))
	dockerLookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	dockerVersionOKFn = func(context.Context) bool { return false }

	err := ensureDockerDaemonForHostOS(context.Background(), dockerHostOSWindows)
	if err == nil {
		t.Fatal("expected docker CLI missing diagnostic")
	}
	msg := err.Error()
	for _, want := range []string{"Docker Desktop is installed", "docker CLI is not on PATH", "repair Docker Desktop CLI tools"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error = %q, want substring %q", msg, want)
		}
	}
}

func TestDetectProjectType_Dockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "docker" {
		t.Errorf("detectProjectType = %q; want %q", got, "docker")
	}
}

func TestDetectProjectType_Containerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM alpine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "docker" {
		t.Errorf("detectProjectType = %q; want %q", got, "docker")
	}
}

func TestDetectProjectType_PackageSwift(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Package.swift"), []byte("// swift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "swift" {
		t.Errorf("detectProjectType = %q; want %q", got, "swift")
	}
}

func TestDetectProjectType_RequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "python" {
		t.Errorf("detectProjectType = %q; want %q", got, "python")
	}
}

func TestDetectProjectType_SetupPy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup.py"), []byte("setup()"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "python" {
		t.Errorf("detectProjectType = %q; want %q", got, "python")
	}
}

func TestDetectProjectType_PyprojectToml(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[tool.poetry]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "python" {
		t.Errorf("detectProjectType = %q; want %q", got, "python")
	}
}

func TestDetectProjectType_Unknown(t *testing.T) {
	dir := t.TempDir()
	if got := mustDetectProjectType(t, dir); got != "unknown" {
		t.Errorf("detectProjectType = %q; want %q", got, "unknown")
	}
}

func TestDetectProjectType_DockerfileTakesPrecedence(t *testing.T) {
	dir := t.TempDir()
	// Create both Dockerfile and requirements.txt; Dockerfile should win.
	for _, name := range []string{"Dockerfile", "requirements.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustDetectProjectType(t, dir); got != "docker" {
		t.Errorf("detectProjectType = %q; want %q (Dockerfile should take precedence)", got, "docker")
	}
}

func TestDetectProjectType_DockerfileVariantOnly(t *testing.T) {
	dir := t.TempDir()
	// No base Dockerfile — only variants.
	for _, name := range []string{"Dockerfile.dev", "Dockerfile.prod"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM alpine"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustDetectProjectType(t, dir); got != "docker" {
		t.Errorf("detectProjectType = %q; want %q (Dockerfile variant should be recognised)", got, "docker")
	}
}

func TestDetectProjectType_XcodeOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "MyApp.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "xcode" {
		t.Errorf("detectProjectType = %q; want %q", got, "xcode")
	}
}

func TestDetectProjectType_SwiftPMWinsOverXcode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Package.swift"), []byte("// swift"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "MyApp.xcodeproj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustDetectProjectType(t, dir); got != "swift" {
		t.Errorf("detectProjectType = %q; want %q (Package.swift should take precedence)", got, "swift")
	}
}

func TestDetectProjectType_MultipleXcodeprojs_Error(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"First.xcodeproj", "Second.xcodeproj"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := detectProjectType(dir)
	if err == nil {
		t.Fatal("detectProjectType expected error for multiple .xcodeproj dirs, got nil")
	}
	if !strings.Contains(err.Error(), "multiple .xcodeproj") {
		t.Errorf("expected 'multiple .xcodeproj' in error, got: %v", err)
	}
}

func TestResolveDetectedBuildOption_PrefersDockerfileOverSwift(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Package.swift (Swift)", Type: "swift", File: "Package.swift"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.Type != "docker" || got.File != "Dockerfile" {
		t.Fatalf("got %+v, want Dockerfile docker option", got)
	}
}

func TestResolveDetectedBuildOption_PrefersContainerfileOverPython(t *testing.T) {
	options := []BuildOption{
		{Label: "Containerfile", Type: "docker", File: "Containerfile"},
		{Label: "requirements.txt (Python)", Type: "python", File: "requirements.txt"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.Type != "docker" || got.File != "Containerfile" {
		t.Fatalf("got %+v, want Containerfile docker option", got)
	}
}

func TestResolveDetectedBuildOption_PrefersDockerfileOverPython(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "requirements.txt (Python)", Type: "python", File: "requirements.txt"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.Type != "docker" || got.File != "Dockerfile" {
		t.Fatalf("got %+v, want Dockerfile docker option", got)
	}
}

func TestPreferredBuildOption_InteractiveMultipleDockerfilesDoesNotAutoPrefer(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Dockerfile.dev", Type: "docker", File: "Dockerfile.dev"},
		{Label: "Package.swift (Swift)", Type: "swift", File: "Package.swift"},
	}

	got := preferredBuildOption(options, true)
	if got != nil {
		t.Fatalf("got %+v, want nil so the picker can choose among Dockerfiles", got)
	}
}

func TestBuildOptionForType_DockerUsesExactDockerfile(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile.dev", Type: "docker", File: "Dockerfile.dev"},
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Package.swift (Swift)", Type: "swift", File: "Package.swift"},
	}

	got, err := buildOptionForType(options, "docker", false)
	if err != nil {
		t.Fatalf("buildOptionForType: %v", err)
	}
	if got == nil || got.File != "Dockerfile" {
		t.Fatalf("got %+v, want Dockerfile", got)
	}
}

// resolveDetectedBuildOption uses term.IsTerminal which returns false in test
// environments (stdin is a pipe). These tests therefore exercise the
// non-interactive code path.

func TestResolveDetectedBuildOption_NonInteractiveMultipleDockerfilesPrefersBase(t *testing.T) {
	options := []BuildOption{
		{Label: "Containerfile", Type: "docker", File: "Containerfile"},
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Dockerfile.dev", Type: "docker", File: "Dockerfile.dev"},
		{Label: "Dockerfile.prod", Type: "docker", File: "Dockerfile.prod"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.File != "Dockerfile" {
		t.Fatalf("got %+v, want base Dockerfile", got)
	}
}

func TestResolveDetectedBuildOption_NonInteractiveContainerfileBase(t *testing.T) {
	options := []BuildOption{
		{Label: "Containerfile.dev", Type: "docker", File: "Containerfile.dev"},
		{Label: "Containerfile", Type: "docker", File: "Containerfile"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.File != "Containerfile" {
		t.Fatalf("got %+v, want base Containerfile", got)
	}
}

func TestResolveDetectedBuildOption_NonInteractiveVariantOnlyDockerfilesPrefersFirst(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile.dev", Type: "docker", File: "Dockerfile.dev"},
		{Label: "Dockerfile.prod", Type: "docker", File: "Dockerfile.prod"},
	}

	got, err := resolveDetectedBuildOption(options, "", "")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.File != "Dockerfile.dev" {
		t.Fatalf("got %+v, want Dockerfile.dev (first variant)", got)
	}
}

func TestResolveDetectedBuildOption_DockerfileFlag(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Dockerfile.dev", Type: "docker", File: "Dockerfile.dev"},
		{Label: "Dockerfile.prod", Type: "docker", File: "Dockerfile.prod"},
		{Label: "Package.swift (Swift)", Type: "swift", File: "Package.swift"},
	}

	got, err := resolveDetectedBuildOption(options, "", "Dockerfile.prod")
	if err != nil {
		t.Fatalf("resolveDetectedBuildOption: %v", err)
	}
	if got == nil || got.File != "Dockerfile.prod" {
		t.Fatalf("got %+v, want Dockerfile.prod", got)
	}
}

func TestResolveDetectedBuildOption_DockerfileFlagNotFound(t *testing.T) {
	options := []BuildOption{
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
	}

	_, err := resolveDetectedBuildOption(options, "", "Dockerfile.missing")
	if err == nil {
		t.Fatal("expected error for missing dockerfile")
	}
}

func TestFilterBuildOptions_LocalProviderKeepsNativeOnly(t *testing.T) {
	options := []BuildOption{
		{Label: "compose.yml (Compose)", Type: "compose", File: "compose.yml"},
		{Label: "Dockerfile", Type: "docker", File: "Dockerfile"},
		{Label: "Package.swift (Swift)", Type: "swift", File: "Package.swift"},
		{Label: "requirements.txt (Python)", Type: "python", File: "requirements.txt"},
	}

	got := filterBuildOptions(options, &providers.LocalProvider{})
	if len(got) != 2 {
		t.Fatalf("filtered options = %+v, want swift and python only", got)
	}
	for _, option := range got {
		if option.Type != "swift" && option.Type != "python" {
			t.Fatalf("filtered options include %q, want only swift/python: %+v", option.Type, got)
		}
	}
}

func TestEnsureProviderSupportsProjectType_LocalRejectsContainerProjects(t *testing.T) {
	oldHintSupported := appleContainerLocalProviderHintSupported
	t.Cleanup(func() {
		appleContainerLocalProviderHintSupported = oldHintSupported
	})
	appleContainerLocalProviderHintSupported = func() bool { return true }

	for _, projectType := range []string{"docker", "compose"} {
		t.Run(projectType, func(t *testing.T) {
			err := ensureProviderSupportsProjectType(&providers.LocalProvider{}, projectType, t.TempDir())
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			for _, want := range []string{providers.LocalDisplayName(), "host-native", "Docker", "--device docker"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error = %q, want substring %q", msg, want)
				}
			}
			if projectType == "docker" && !strings.Contains(msg, "--device apple-container") {
				t.Fatalf("docker error = %q, want Apple Container hint on supported hosts", msg)
			}
			if projectType == "compose" && strings.Contains(msg, "apple-container") {
				t.Fatalf("compose error = %q, must not suggest Apple Container", msg)
			}
		})
	}
}

func TestEnsureProviderSupportsProjectType_LocalOmitsAppleContainerHintWhenUnsupported(t *testing.T) {
	oldHintSupported := appleContainerLocalProviderHintSupported
	t.Cleanup(func() {
		appleContainerLocalProviderHintSupported = oldHintSupported
	})
	appleContainerLocalProviderHintSupported = func() bool { return false }

	err := ensureProviderSupportsProjectType(&providers.LocalProvider{}, "docker", t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "apple-container") {
		t.Fatalf("error = %q, must not suggest Apple Container on unsupported hosts", err)
	}
}

func TestEnsureProviderSupportsProjectType_DockerProviderAllowsSwiftImageBuilder(t *testing.T) {
	if err := ensureProviderSupportsProjectType(&providers.DockerProvider{}, "swift", t.TempDir()); err != nil {
		t.Fatalf("DockerProvider should support swift via ImageBuilder: %v", err)
	}
}

func TestEnsureProviderSupportsProjectType_LocalAllowsUnknownGoProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureProviderSupportsProjectType(&providers.LocalProvider{}, "unknown", dir); err != nil {
		t.Fatalf("LocalProvider should allow unknown project types it can build directly: %v", err)
	}
}

func TestResolveRunDockerfile_SingleDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.prod"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile.prod" {
		t.Fatalf("got %q, want Dockerfile.prod", got)
	}
}

func TestResolveRunDockerfile_ExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Dockerfile.prod"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveDockerfile(dir, "Dockerfile.prod", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile.prod" {
		t.Fatalf("got %q, want Dockerfile.prod", got)
	}
}

func TestResolveRunDockerfile_MultipleNonInteractivePrefersBase(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Dockerfile.dev", "Dockerfile.prod"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile" {
		t.Fatalf("got %q, want Dockerfile", got)
	}
}

func TestResolveRunProjectType_DockerfileVariantOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.prod"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveRunProjectType(dir, "docker")
	if err != nil {
		t.Fatalf("resolveRunProjectType: %v", err)
	}
	if got != "docker" {
		t.Fatalf("got %q, want docker", got)
	}
}

func TestResolveRunProjectType_DefaultPrefersDockerfile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Package.swift"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveRunProjectType(dir, "")
	if err != nil {
		t.Fatalf("resolveRunProjectType: %v", err)
	}
	if got != "docker" {
		t.Fatalf("got %q, want docker", got)
	}
}

func TestResolveRunProjectType_SwiftOverride(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Package.swift"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveRunProjectType(dir, "swift")
	if err != nil {
		t.Fatalf("resolveRunProjectType: %v", err)
	}
	if got != "swift" {
		t.Fatalf("got %q, want swift", got)
	}
}

func TestResolveRegistryForAgentUsesConnectionDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var dialedPort int
	conn := &grpcclient.AgentConnection{
		Host: "cloud-device-name",
		RegistryDialer: func(_ context.Context, port int) (net.Conn, error) {
			dialedPort = port
			proxySide, registrySide := net.Pipe()
			go func() {
				defer registrySide.Close()
				buf := make([]byte, 16)
				n, err := registrySide.Read(buf)
				if err == nil && n > 0 {
					_, _ = registrySide.Write(buf[:n])
				}
			}()
			return proxySide, nil
		},
	}

	registryAddr, cleanup, err := resolveRegistryForAgent(ctx, conn, 5000)
	if err != nil {
		t.Fatalf("resolveRegistryForAgent: %v", err)
	}
	defer cleanup()

	_, port, err := net.SplitHostPort(registryAddr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", registryAddr, err)
	}
	tcpConn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer tcpConn.Close()

	if _, err := tcpConn.Write([]byte("ping")); err != nil {
		t.Fatalf("write proxy: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := tcpConn.Read(buf); err != nil {
		t.Fatalf("read proxy: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("proxy echoed %q, want ping", string(buf))
	}
	if dialedPort != 5000 {
		t.Fatalf("dialed port = %d, want 5000", dialedPort)
	}
}

func TestResolveRunProjectType_PythonOverride(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "requirements.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := resolveRunProjectType(dir, "python")
	if err != nil {
		t.Fatalf("resolveRunProjectType: %v", err)
	}
	if got != "python" {
		t.Fatalf("got %q, want python", got)
	}
}

func TestResolveRunProjectType_InvalidOverride(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveRunProjectType(dir, "ruby")
	if err == nil {
		t.Fatal("expected error for invalid run build type override")
	}
	if !strings.Contains(err.Error(), `invalid value "ruby" for --build-type`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveRunProjectType_PropagatesMarkerStatErrors(t *testing.T) {
	dir := t.TempDir()
	notDir := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveRunProjectType(notDir, "docker")
	if err == nil {
		t.Fatal("expected stat error for invalid project path")
	}
	if !strings.Contains(err.Error(), "checking for") {
		t.Fatalf("expected wrapped stat error, got %v", err)
	}
}

func TestGeneratePythonDockerfile_WithRequirements(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := generatePythonDockerfile(dir, false)
	if err != nil {
		t.Fatalf("generatePythonDockerfile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated Dockerfile: %v", err)
	}
	content := string(data)

	expectations := []string{
		"FROM python:3.11-slim",
		"WORKDIR /app",
		"COPY requirements.txt .",
		"RUN pip install --no-cache-dir -r requirements.txt",
		"COPY . .",
		`CMD ["python", "app.py"]`,
	}
	for _, exp := range expectations {
		if !strings.Contains(content, exp) {
			t.Errorf("generated Dockerfile missing %q\nGot:\n%s", exp, content)
		}
	}
}

func TestGeneratePythonDockerfile_WithoutRequirements_MainPy(t *testing.T) {
	dir := t.TempDir()
	// Only main.py, no requirements.txt, no app.py.
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, err := generatePythonDockerfile(dir, false)
	if err != nil {
		t.Fatalf("generatePythonDockerfile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading generated Dockerfile: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "requirements.txt") {
		t.Error("Dockerfile should not mention requirements.txt when it does not exist")
	}
	if !strings.Contains(content, `CMD ["python", "main.py"]`) {
		t.Errorf("expected CMD with main.py, got:\n%s", content)
	}
}

func TestGeneratePythonDockerfile_FallbackEntrypoint(t *testing.T) {
	dir := t.TempDir()
	// No app.py or main.py; should fall back to app.py as default.

	_, err := generatePythonDockerfile(dir, false)
	if err != nil {
		t.Fatalf("generatePythonDockerfile: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `CMD ["python", "app.py"]`) {
		t.Errorf("expected fallback to app.py entrypoint, got:\n%s", string(data))
	}
}

func TestRegistryHost_IPv4(t *testing.T) {
	got := registryHost("192.168.1.5", 5000)
	if got != "192.168.1.5:5000" {
		t.Errorf("registryHost IPv4 = %q, want %q", got, "192.168.1.5:5000")
	}
}

func TestRegistryHost_IPv6Global(t *testing.T) {
	got := registryHost("2001:db8::1", 5000)
	if got != "[2001:db8::1]:5000" {
		t.Errorf("registryHost IPv6 global = %q, want %q", got, "[2001:db8::1]:5000")
	}
}

func TestRegistryHost_IPv6LinkLocalWithZone(t *testing.T) {
	got := registryHost("fe80::2ecf:67ff:feba:6cca%en0", 5000)
	// Zone ID must be stripped — it's host-specific and unusable in containers.
	if got != "[fe80::2ecf:67ff:feba:6cca]:5000" {
		t.Errorf("registryHost IPv6 link-local+zone = %q, want %q", got, "[fe80::2ecf:67ff:feba:6cca]:5000")
	}
}

func TestRegistryHost_IPv6LinkLocalNoZone(t *testing.T) {
	got := registryHost("fe80::1", 5000)
	if got != "[fe80::1]:5000" {
		t.Errorf("registryHost IPv6 link-local no zone = %q, want %q", got, "[fe80::1]:5000")
	}
}

func TestSplitIPv6RegistryAddr_IPv6WithZone(t *testing.T) {
	eff, ip := splitIPv6RegistryAddr("[fe80::1%en0]:5000")
	if eff != "wendy-registry:5000" {
		t.Errorf("effectiveAddr = %q, want %q", eff, "wendy-registry:5000")
	}
	if ip != "fe80::1" {
		t.Errorf("ipv6IP = %q, want %q (zone stripped)", ip, "fe80::1")
	}
}

func TestSplitIPv6RegistryAddr_IPv6NoZone(t *testing.T) {
	eff, ip := splitIPv6RegistryAddr("[2001:db8::1]:5000")
	if eff != "wendy-registry:5000" {
		t.Errorf("effectiveAddr = %q, want %q", eff, "wendy-registry:5000")
	}
	if ip != "2001:db8::1" {
		t.Errorf("ipv6IP = %q, want %q", ip, "2001:db8::1")
	}
}

func TestSplitIPv6RegistryAddr_IPv4Passthrough(t *testing.T) {
	eff, ip := splitIPv6RegistryAddr("192.168.1.5:5000")
	if eff != "192.168.1.5:5000" {
		t.Errorf("effectiveAddr = %q, want unchanged", eff)
	}
	if ip != "" {
		t.Errorf("ipv6IP = %q, want empty for IPv4", ip)
	}
}

func TestSplitIPv6RegistryAddr_HostnamePassthrough(t *testing.T) {
	eff, ip := splitIPv6RegistryAddr("wendy-registry:5000")
	if eff != "wendy-registry:5000" {
		t.Errorf("effectiveAddr = %q, want unchanged", eff)
	}
	if ip != "" {
		t.Errorf("ipv6IP = %q, want empty for hostname", ip)
	}
}

func TestResolveRegistryIP_StripZone(t *testing.T) {
	got := resolveRegistryIP("fe80::1%eth0")
	if got != "fe80::1" {
		t.Errorf("resolveRegistryIP zone = %q, want %q", got, "fe80::1")
	}
}

func TestResolveRegistryIP_IPv4Passthrough(t *testing.T) {
	got := resolveRegistryIP("10.0.0.1")
	if got != "10.0.0.1" {
		t.Errorf("resolveRegistryIP IPv4 = %q, want %q", got, "10.0.0.1")
	}
}

func TestIsLinkLocalIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"169.254.1.1", true},
		{"169.254.189.250", true},
		{"192.168.1.5", false},
		{"10.0.0.1", false},
		{"fe80::1", true},
		{"[fe80::1]", true},
		{"2001:db8::1", false},
		{"not-an-ip", false},
	}
	for _, tt := range tests {
		if got := isLinkLocalIP(tt.ip); got != tt.want {
			t.Errorf("isLinkLocalIP(%q) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestStartRegistryProxy(t *testing.T) {
	// Start a fake "registry" server.
	fakeRegistry := make(chan string, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		fakeRegistry <- string(buf[:n])
		conn.Write([]byte("OK"))
	}()

	// Start the proxy pointing at the fake registry.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, err := startRegistryProxy(ctx, "127.0.0.1:0", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	// Connect through the proxy.
	conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(proxy.Port()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("PUSH")); err != nil {
		t.Fatal(err)
	}

	var got string
	select {
	case got = <-fakeRegistry:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not forward request")
	}
	if got != "PUSH" {
		t.Errorf("proxy forwarded %q, want %q", got, "PUSH")
	}
}

// TestRegistryProxyBindsLoopback guards WDY-1168: the registry proxy must bind
// loopback only so the device registry tunnel is never exposed on other network
// interfaces during a build. A regression to "0.0.0.0:0" binds the unspecified
// address, which is not loopback and fails this test.
func TestRegistryProxyBindsLoopback(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, err := startRegistryProxy(ctx, registryProxyListenAddr, target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()

	ip := proxy.listener.Addr().(*net.TCPAddr).IP
	if !ip.IsLoopback() {
		t.Fatalf("registry proxy bound to %s, want a loopback address", ip)
	}
}

func TestFindIPv4ViaNeighborTable_UnknownAddress(t *testing.T) {
	// This test would invoke findIPv4ViaNeighborTable, which may spawn real ndp/arp/ip
	// commands and read the host's neighbor tables, making it environment-dependent.
	// Skip it to avoid flakiness/timeouts in unit test environments.
	t.Skip("disabled: findIPv4ViaNeighborTable depends on host neighbor tables and OS commands")
}

// testCert holds a certificate and its signing key for use in TLS test setups.
type testCert struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	pemStr string
}

// generateTestCA creates a self-signed CA certificate.
func generateTestCA(t *testing.T) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-root-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	return &testCert{
		cert:   cert,
		key:    key,
		pemStr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// generateTestIntermediate creates an intermediate CA signed by the given parent CA.
func generateTestIntermediate(t *testing.T, parent *testCert) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate intermediate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "test-intermediate-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent.cert, &key.PublicKey, parent.key)
	if err != nil {
		t.Fatalf("create intermediate cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse intermediate cert: %v", err)
	}
	return &testCert{
		cert:   cert,
		key:    key,
		pemStr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// generateTestLeafNoSAN creates a server-auth leaf certificate with no SANs,
// simulating a device cert that lacks a hostname or IP SAN (the common Wendy case).
func generateTestLeafNoSAN(t *testing.T, issuer *testCert) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "test-leaf-no-san"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		// Deliberately no IPAddresses or DNSNames.
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer.cert, &key.PublicKey, issuer.key)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	return &testCert{
		cert:   cert,
		key:    key,
		pemStr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// generateTestLeaf creates a leaf certificate signed by the given issuer.
// eku controls the ExtKeyUsage (use x509.ExtKeyUsageServerAuth or x509.ExtKeyUsageClientAuth).
// IPAddresses is populated only for server certs (ServerAuth) so hostname verification works.
func generateTestLeaf(t *testing.T, issuer *testCert, eku x509.ExtKeyUsage) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "test-leaf"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
	}
	if eku == x509.ExtKeyUsageServerAuth {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer.cert, &key.PublicKey, issuer.key)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	return &testCert{
		cert:   cert,
		key:    key,
		pemStr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// marshalKeyPEM encodes an ECDSA private key to PEM.
func marshalKeyPEM(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}))
}

// startTestTLSServer starts a TLS HTTPS server that responds with 200 OK.
// tlsCert configures the server's certificate chain (leaf + optional intermediates).
// clientCA, if non-nil, enables mutual TLS and requires client certs signed by it.
func startTestTLSServer(t *testing.T, tlsCert tls.Certificate, clientCA *testCert) string {
	t.Helper()
	cfg := &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	if clientCA != nil {
		pool := x509.NewCertPool()
		pool.AddCert(clientCA.cert)
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestStartMTLSRegistryHTTPProxy_DirectCA(t *testing.T) {
	ca := generateTestCA(t)
	serverLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageServerAuth)
	clientLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	addr := startTestTLSServer(t, serverTLSCert, ca)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), ca.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStartMTLSRegistryHTTPProxy_IntermediateChain(t *testing.T) {
	root := generateTestCA(t)
	intermediate := generateTestIntermediate(t, root)
	serverLeaf := generateTestLeaf(t, intermediate, x509.ExtKeyUsageServerAuth)
	clientLeaf := generateTestLeaf(t, root, x509.ExtKeyUsageClientAuth)

	// Server sends leaf + intermediate in the TLS handshake.
	serverTLSCert := tls.Certificate{
		Certificate: [][]byte{serverLeaf.cert.Raw, intermediate.cert.Raw},
		PrivateKey:  serverLeaf.key,
	}
	addr := startTestTLSServer(t, serverTLSCert, root)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), root.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestStartMTLSRegistryHTTPProxy_WrongCA(t *testing.T) {
	trustedCA := generateTestCA(t)
	untrustedCA := generateTestCA(t)
	serverLeaf := generateTestLeaf(t, untrustedCA, x509.ExtKeyUsageServerAuth)
	clientLeaf := generateTestLeaf(t, trustedCA, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	// Don't require client certs on the server side so we test the proxy's cert verification.
	addr := startTestTLSServer(t, serverTLSCert, nil)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), trustedCA.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		// Transport-level TLS rejection surfaces as a connection error via the proxy.
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 when server cert is signed by untrusted CA, got 200")
	}
}

func TestStartMTLSRegistryHTTPProxy_ClientAuthOnlyServerCert(t *testing.T) {
	// Wendy device registry certs are mutual-auth identity certs that chain to
	// the Wendy CA but may be issued with only a clientAuth EKU (no serverAuth).
	// The proxy must accept them via chain validation rather than rejecting on
	// EKU — otherwise the Apple Container push 502s with "incompatible key usage".
	ca := generateTestCA(t)
	serverLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageClientAuth)
	clientLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	addr := startTestTLSServer(t, serverTLSCert, ca)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), ca.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (clientAuth-only server cert should be accepted via chain validation)", resp.StatusCode)
	}
}

func TestStartMTLSRegistryHTTPProxy_RejectsNonAuthCert(t *testing.T) {
	// A leaf signed by the trusted CA but carrying a non-authentication EKU
	// (codeSigning) must be rejected: the proxy accepts only serverAuth/clientAuth
	// identity certs, so it cannot be abused to impersonate the registry.
	ca := generateTestCA(t)
	serverLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageCodeSigning)
	clientLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	// Don't require client certs so we isolate the proxy's server-cert check.
	addr := startTestTLSServer(t, serverTLSCert, nil)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), ca.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		return // TLS rejection surfaced as a connection error — acceptable
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 for a non-authentication (codeSigning) server cert, got 200")
	}
}

func TestStartMTLSRegistryHTTPProxy_NoSAN(t *testing.T) {
	// Device certs signed by the Wendy CA often lack a SAN for the target
	// mDNS hostname. Verify the proxy accepts such certs via chain validation
	// (VerifyConnection) while InsecureSkipVerify bypasses hostname checks.
	ca := generateTestCA(t)
	serverLeaf := generateTestLeafNoSAN(t, ca)
	clientLeaf := generateTestLeaf(t, ca, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	addr := startTestTLSServer(t, serverTLSCert, ca)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), ca.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (cert without SAN should be accepted via chain validation)", resp.StatusCode)
	}
}

func TestStartMTLSRegistryHTTPProxy_UntrustedClientCert(t *testing.T) {
	// The server requires a client cert from trustedCA, but the proxy presents
	// one from untrustedCA. The server should reject the connection.
	trustedCA := generateTestCA(t)
	untrustedCA := generateTestCA(t)
	serverLeaf := generateTestLeaf(t, trustedCA, x509.ExtKeyUsageServerAuth)
	clientLeaf := generateTestLeaf(t, untrustedCA, x509.ExtKeyUsageClientAuth)

	serverTLSCert, err := tls.X509KeyPair([]byte(serverLeaf.pemStr), []byte(marshalKeyPEM(t, serverLeaf.key)))
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	// Server requires client certs signed by trustedCA.
	addr := startTestTLSServer(t, serverTLSCert, trustedCA)

	proxy, err := startMTLSRegistryHTTPProxy(addr, clientLeaf.pemStr, marshalKeyPEM(t, clientLeaf.key), trustedCA.pemStr)
	if err != nil {
		t.Fatalf("startMTLSRegistryHTTPProxy: %v", err)
	}
	defer proxy.Close()

	resp, err := http.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(proxy.Port())))
	if err != nil {
		// Transport-level rejection from the server is acceptable.
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 when proxy presents a client cert from an untrusted CA, got 200")
	}
}

func TestResolveDockerfile_NoDockerfiles(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestResolveDockerfile_RequestedPassthrough(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile", "Dockerfile.prod"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveDockerfile(dir, "Dockerfile.prod", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile.prod" {
		t.Fatalf("got %q, want Dockerfile.prod", got)
	}
}

func TestResolveDockerfile_SingleVariant(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.dev"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile.dev" {
		t.Fatalf("got %q, want Dockerfile.dev", got)
	}
}

func TestResolveDockerfile_Containerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Containerfile" {
		t.Fatalf("got %q, want Containerfile", got)
	}
}

func TestResolveDockerfile_MultipleNonInteractivePrefersBase(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Containerfile", "Dockerfile", "Dockerfile.prod", "Dockerfile.dev"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got != "Dockerfile" {
		t.Fatalf("got %q, want Dockerfile", got)
	}
}

func TestResolveDockerfile_MultipleNonInteractiveVariantOnlyPrefersFirst(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Dockerfile.dev", "Dockerfile.prod"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveDockerfile(dir, "", false)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if got == "" {
		t.Fatal("got empty, want a Dockerfile variant")
	}
}

func TestValidateDockerfileName(t *testing.T) {
	valid := []string{"Dockerfile", "Dockerfile.prod", "Dockerfile.dev", "Dockerfile-prod", "Dockerfile.my.variant", "./Dockerfile.prod", "Containerfile", "Containerfile.prod", "Containerfile-dev"}
	for _, name := range valid {
		if err := validateDockerfileName(name); err != nil {
			t.Errorf("validateDockerfileName(%q) unexpected error: %v", name, err)
		}
	}
	invalid := []string{"-flag", "dockerfile", "containerfile", "DOCKERFILE", "CONTAINERFILE", "not-a-dockerfile", "Dockerfile/evil", "subdir/Dockerfile.prod", "../Dockerfile", ".hidden", "Dockerfile.dockerignore", "Dockerfile.prod.dockerignore", "Containerfile.dockerignore", "Dockerfile.-prod", "Dockerfile..hidden", "Dockerfile-.prod", "Containerfile.-prod"}
	for _, name := range invalid {
		if err := validateDockerfileName(name); err == nil {
			t.Errorf("validateDockerfileName(%q) expected error, got nil", name)
		}
	}
}

func TestValidateBuildArgPair(t *testing.T) {
	valid := map[string]string{
		"WENDY_PLATFORM":    "nvidia-jetson",
		"_PRIVATE_ARG":      "value-without-equals",
		"WENDY_DEVICE_TYPE": "jetson-agx-orin",
	}
	for k, v := range valid {
		if err := validateBuildArgPair(k, v); err != nil {
			t.Fatalf("validateBuildArgPair(%q, %q): %v", k, v, err)
		}
	}

	invalid := map[string]string{
		"":          "value",
		"BAD-NAME":  "value",
		"1BAD":      "value",
		"BAD=NAME":  "value",
		"BAD\nKEY":  "value",
		"GOOD":      "bad\nvalue",
		"ALSO_GOOD": "bad\x00value",
		"LEADING":   "--flag-like",
		"SHELL":     "$(echo bad)",
		"DIGEST":    "image@sha256:abc",
		"PLUS":      "v1+metadata",
		"EQUALS":    "value=with=equals",
		"SLASH":     "linux/arm64",
		"COLON":     "8080:80",
		"COMMA":     "left,right",
		"COMMAFLAG": ",--cache",
	}
	for k, v := range invalid {
		if err := validateBuildArgPair(k, v); err == nil {
			t.Fatalf("validateBuildArgPair(%q, %q) = nil, want error", k, v)
		}
	}
}

func TestConfinedDockerfilePath_Traversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := confinedDockerfilePath(dir, "../../etc/passwd"); err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

func TestConfinedDockerfilePath_NotExist(t *testing.T) {
	dir := t.TempDir()
	if _, err := confinedDockerfilePath(dir, "Dockerfile.notexist"); err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestConfinedDockerfilePath_Valid(t *testing.T) {
	dir := t.TempDir()
	name := "Dockerfile.prod"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := confinedDockerfilePath(dir, name)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty resolved path")
	}
}

func TestAppleContainerBuildFilePathUsesTmpAliasWhenAvailable(t *testing.T) {
	oldGOOS := imageBuilderHostGOOS
	t.Cleanup(func() {
		imageBuilderHostGOOS = oldGOOS
	})
	imageBuilderHostGOOS = func() string { return "darwin" }

	dir, err := os.MkdirTemp("/tmp", "wendy-apple-buildfile.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	privateDir := "/private" + dir
	dirCanonical, dirErr := filepath.EvalSymlinks(dir)
	privateCanonical, privateErr := filepath.EvalSymlinks(privateDir)
	if dirErr != nil || privateErr != nil || dirCanonical != privateCanonical {
		t.Skip("/private/tmp is not an alias for /tmp on this host")
	}

	got, err := appleContainerBuildFilePath(privateDir, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "Dockerfile")
	if got != want {
		t.Fatalf("build file path = %q, want %q", got, want)
	}
}

// TestConfinedDockerfilePath_DoubleDotPrefixDirAllowed guards against the
// HasPrefix(rel, "..") regression: a child directory whose name starts with
// ".." (like "..cache") must not be mistaken for a parent-directory reference.
func TestConfinedDockerfilePath_DoubleDotPrefixDirAllowed(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "..cache")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := confinedDockerfilePath(dir, filepath.Join("..cache", "Dockerfile"))
	if err != nil {
		t.Fatalf("confinedDockerfilePath: %v", err)
	}
	if got == "" {
		t.Fatal("expected non-empty resolved path")
	}
}

func TestConfinedDockerfilePath_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	// Create a Dockerfile in the target (outside dir).
	if err := os.WriteFile(filepath.Join(target, "Dockerfile"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink inside dir pointing to target dir.
	linkPath := filepath.Join(dir, "link")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if _, err := confinedDockerfilePath(dir, "link/Dockerfile"); err == nil {
		t.Fatal("expected error for symlink escape, got nil")
	}
}

// TestResolveDockerfile_AutoSelectionRejectsSymlinkEscape verifies that the
// auto-selection path (no explicit --dockerfile) applies confinement checks,
// so a Dockerfile symlink pointing outside the project is rejected.
func TestResolveDockerfile_AutoSelectionRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	// Create the real Dockerfile outside the project.
	if err := os.WriteFile(filepath.Join(outside, "contents"), []byte("FROM scratch"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Place a Dockerfile.prod symlink inside the project pointing outside.
	link := filepath.Join(dir, "Dockerfile.prod")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	if _, err := resolveDockerfile(dir, "", false); err == nil {
		t.Fatal("expected error for auto-selected symlink escape, got nil")
	}
}
