package providers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/models"
)

func TestAppleContainerSupportedHost(t *testing.T) {
	if !appleContainerSupportedHost("darwin", "arm64") {
		t.Fatal("darwin/arm64 should support Apple Container")
	}
	for _, tc := range []struct {
		goos   string
		goarch string
	}{
		{"darwin", "amd64"},
		{"linux", "arm64"},
		{"windows", "arm64"},
	} {
		if appleContainerSupportedHost(tc.goos, tc.goarch) {
			t.Fatalf("%s/%s should not support Apple Container", tc.goos, tc.goarch)
		}
	}
}

func TestAppleContainerInspectHasManagedLabel(t *testing.T) {
	managed := []byte(`{"id":"app","configuration":{"labels":{"wendy.managed":"true"}}}`)
	if !appleContainerInspectHasManagedLabel(managed) {
		t.Fatal("expected managed label to be detected")
	}

	unmanaged := []byte(`{"id":"app","configuration":{"labels":{"other":"true"}}}`)
	if appleContainerInspectHasManagedLabel(unmanaged) {
		t.Fatal("unexpected managed label")
	}

	malformed := []byte(`container wendy.managed true`)
	if appleContainerInspectHasManagedLabel(malformed) {
		t.Fatal("malformed inspect output must not be treated as managed")
	}

	deepManaged := []byte(strings.Repeat(`{"nested":`, appleContainerMaxJSONDepth+2) + `{"wendy.managed":"true"}` + strings.Repeat(`}`, appleContainerMaxJSONDepth+2))
	if appleContainerInspectHasManagedLabel(deepManaged) {
		t.Fatal("deeply nested inspect output must not be treated as managed")
	}

	oversized := []byte(strings.Repeat(" ", appleContainerMaxJSONOutputBytes+1))
	if appleContainerInspectHasManagedLabel(oversized) {
		t.Fatal("oversized inspect output must not be treated as managed")
	}
}

func TestAppleContainerListInfos(t *testing.T) {
	got := appleContainerListInfos([]byte(`[
		{"id":"app","image":"app:latest","state":"running"},
		{"ID":"other","Image":"other:latest","State":"stopped","Status":"exited"},
		{
			"id":"nested",
			"configuration":{"image":{"reference":"nested:latest"}},
			"status":{"state":"running"}
		}
	]`))
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Name != "app" || got[0].Image != "app:latest" || got[0].State != "running" {
		t.Fatalf("first entry = %+v", got[0])
	}
	if got[1].Name != "other" || got[1].Status != "exited" {
		t.Fatalf("second entry = %+v", got[1])
	}
	if got[2].Name != "nested" || got[2].Image != "nested:latest" || got[2].State != "running" || got[2].Status != "running" {
		t.Fatalf("third entry = %+v", got[2])
	}
}

func TestValidateAppleContainerKeyValueArg(t *testing.T) {
	valid := map[string]string{
		"wendy.managed":                  "true",
		"sh.wendy/entitlement.network":   "mode=host,ports=8080:80",
		"sh.wendy/entitlement.persist.0": "name=data,path=/app/data",
		"sh.wendy/entitlement.gpu":       "",
	}
	for k, v := range valid {
		if err := validateAppleContainerKeyValueArg("label", k, v); err != nil {
			t.Fatalf("validateAppleContainerKeyValueArg(%q, %q): %v", k, v, err)
		}
	}

	invalid := map[string]string{
		"":             "true",
		"bad=key":      "true",
		"bad\nkey":     "true",
		"bad/key/part": "true",
		"bad key":      "true",
		"good.key":     "bad\nvalue",
		"also.good":    "bad\rvalue",
		"another.good": "bad\x00value",
		"unicode.good": "snowman-☃",
		"shell.good":   "$(echo bad)",
		"digest.good":  "image@sha256:abc",
		"plus.good":    "v1+metadata",
		"pipe.good":    "left|right",
		"semi.good":    "left;right",
		"amp.good":     "left&right",
		"quote.good":   `left"right`,
		"back.good":    `left\right`,
	}
	for k, v := range invalid {
		if err := validateAppleContainerKeyValueArg("label", k, v); err == nil {
			t.Fatalf("validateAppleContainerKeyValueArg(%q, %q) = nil, want error", k, v)
		}
	}
}

func TestValidateAppleContainerContainerName(t *testing.T) {
	for _, name := range []string{"myapp", "sh.wendy.app", "App_1-prod"} {
		if err := validateAppleContainerContainerName(name); err != nil {
			t.Fatalf("validateAppleContainerContainerName(%q): %v", name, err)
		}
	}
	for _, name := range []string{"", "--flag", "bad/name", "bad name", "☃"} {
		if err := validateAppleContainerContainerName(name); err == nil {
			t.Fatalf("validateAppleContainerContainerName(%q) = nil, want error", name)
		}
	}
}

func TestAppleContainerBuildWithDockerfileUsesContainerBuild(t *testing.T) {
	restore := stubAppleContainerHost(t)
	defer restore()

	logFile := filepath.Join(t.TempDir(), "commands.log")
	oldCommand := appleContainerCommandContext
	oldLookPath := appleContainerLookPath
	t.Cleanup(func() {
		appleContainerCommandContext = oldCommand
		appleContainerLookPath = oldLookPath
	})
	appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)
	appleContainerLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}

	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile.prod")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildContext, err := appleContainerBuildContextPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDockerfile, err := filepath.EvalSymlinks(dockerfile)
	if err != nil {
		t.Fatal(err)
	}

	p := &AppleContainerProvider{}
	app, err := p.BuildWithDockerfile(context.Background(), models.ExternalDevice{CPUArchitecture: "arm64"}, dir, "MyApp", "docker", "Dockerfile.prod", false)
	if err != nil {
		t.Fatalf("BuildWithDockerfile: %v", err)
	}
	if app.ProviderKey != ProviderKeyAppleContainer {
		t.Fatalf("ProviderKey = %q", app.ProviderKey)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	for _, want := range []string{
		"container\x00--version\n",
		"container\x00system\x00status\n",
		"container\x00build\x00--platform\x00linux/arm64\x00-t\x00myapp:latest\x00-f\x00" + resolvedDockerfile + "\x00" + buildContext + "\n",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("command log missing %q in:\n%s", want, log)
		}
	}
}

func TestAppleContainerBuildWithContainerfileUsesContainerBuild(t *testing.T) {
	restore := stubAppleContainerHost(t)
	defer restore()

	logFile := filepath.Join(t.TempDir(), "commands.log")
	oldCommand := appleContainerCommandContext
	oldLookPath := appleContainerLookPath
	t.Cleanup(func() {
		appleContainerCommandContext = oldCommand
		appleContainerLookPath = oldLookPath
	})
	appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)
	appleContainerLookPath = func(file string) (string, error) {
		if file == "container" {
			return "/usr/local/bin/container", nil
		}
		return "", errors.New("not found")
	}

	dir := t.TempDir()
	containerfile := filepath.Join(dir, "Containerfile")
	if err := os.WriteFile(containerfile, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildContext, err := appleContainerBuildContextPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedContainerfile, err := filepath.EvalSymlinks(containerfile)
	if err != nil {
		t.Fatal(err)
	}

	p := &AppleContainerProvider{}
	if !p.CanBuild(dir) {
		t.Fatal("CanBuild = false, want true for Containerfile")
	}
	if _, err := p.Build(context.Background(), models.ExternalDevice{CPUArchitecture: "arm64"}, dir, "MyApp", false); err != nil {
		t.Fatalf("Build: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "container\x00build\x00--platform\x00linux/arm64\x00-t\x00myapp:latest\x00-f\x00" + resolvedContainerfile + "\x00" + buildContext + "\n"
	if !strings.Contains(string(data), want) {
		t.Fatalf("command log missing %q in:\n%s", want, string(data))
	}
}

func TestAppleContainerBuildContextUsesTmpAliasWhenAvailable(t *testing.T) {
	restore := stubAppleContainerHost(t)
	defer restore()

	dir, err := os.MkdirTemp("/tmp", "wendy-apple-context.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	privateDir := "/private" + dir
	dirCanonical, dirErr := filepath.EvalSymlinks(dir)
	privateCanonical, privateErr := filepath.EvalSymlinks(privateDir)
	if dirErr != nil || privateErr != nil || dirCanonical != privateCanonical {
		t.Skip("/private/tmp is not an alias for /tmp on this host")
	}

	got, err := appleContainerBuildContextPath(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("build context = %q, want %q", got, dir)
	}
}

func TestConfinedProviderDockerfilePathUsesTmpAliasWhenAvailable(t *testing.T) {
	restore := stubAppleContainerHost(t)
	defer restore()

	dir, err := os.MkdirTemp("/tmp", "wendy-apple-dockerfile.")
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

	got, err := confinedProviderDockerfilePath(privateDir, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "Dockerfile")
	if got != want {
		t.Fatalf("dockerfile path = %q, want %q", got, want)
	}
}

func TestConfinedProviderDockerfilePathRejectsSubpaths(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := confinedProviderDockerfilePath(dir, filepath.Join("sub", "Dockerfile")); err == nil {
		t.Fatal("expected subpath Dockerfile to be rejected")
	}
}

func TestAppleContainerStopIncludesStderr(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "commands.log")
	oldCommand := appleContainerCommandContext
	t.Cleanup(func() {
		appleContainerCommandContext = oldCommand
	})
	t.Setenv("APPLE_CONTAINER_HELPER_STOP_ERROR", "permission denied")
	appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)

	err := (&AppleContainerProvider{}).Stop(context.Background(), &BuiltApp{
		Context: &appleContainerBuildContext{ContainerName: "myapp"},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Stop error = %v, want stderr context", err)
	}
}

func TestRemoveManagedContainerInspectErrorHandling(t *testing.T) {
	t.Run("not found is ignored", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "commands.log")
		oldCommand := appleContainerCommandContext
		t.Cleanup(func() {
			appleContainerCommandContext = oldCommand
		})
		t.Setenv("APPLE_CONTAINER_HELPER_INSPECT", "missing")
		appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)

		if err := (&AppleContainerProvider{}).removeManagedContainer(context.Background(), "myapp"); err != nil {
			t.Fatalf("removeManagedContainer: %v", err)
		}
	})

	t.Run("other inspect errors are returned", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "commands.log")
		oldCommand := appleContainerCommandContext
		t.Cleanup(func() {
			appleContainerCommandContext = oldCommand
		})
		t.Setenv("APPLE_CONTAINER_HELPER_INSPECT", "error")
		appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)

		err := (&AppleContainerProvider{}).removeManagedContainer(context.Background(), "myapp")
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("removeManagedContainer error = %v, want inspect stderr context", err)
		}
	})

	t.Run("oversized inspect output is rejected", func(t *testing.T) {
		logFile := filepath.Join(t.TempDir(), "commands.log")
		oldCommand := appleContainerCommandContext
		t.Cleanup(func() {
			appleContainerCommandContext = oldCommand
		})
		t.Setenv("APPLE_CONTAINER_HELPER_INSPECT", "large")
		appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)

		err := (&AppleContainerProvider{}).removeManagedContainer(context.Background(), "myapp")
		if err == nil || !strings.Contains(err.Error(), "output exceeds") {
			t.Fatalf("removeManagedContainer error = %v, want output limit error", err)
		}
	})
}

func TestAppleContainerListContainersReturnsInspectErrors(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "commands.log")
	oldCommand := appleContainerCommandContext
	t.Cleanup(func() {
		appleContainerCommandContext = oldCommand
	})
	t.Setenv("APPLE_CONTAINER_HELPER_LIST", "one")
	t.Setenv("APPLE_CONTAINER_HELPER_INSPECT", "error")
	appleContainerCommandContext = fakeAppleContainerCommandContext(logFile)

	_, err := (&AppleContainerProvider{}).ListContainers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("ListContainers error = %v, want inspect stderr context", err)
	}
}

func TestAppleContainerCombinedOutputLimitedTruncates(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestAppleContainerHelperProcess", "--", "container", "inspect", "myapp")
	cmd.Env = append(os.Environ(),
		"GO_WANT_APPLE_CONTAINER_HELPER_PROCESS=1",
		"APPLE_CONTAINER_HELPER_INSPECT=large",
	)
	out, truncated, err := appleContainerCombinedOutputLimited(cmd, 1024)
	if err != nil {
		t.Fatalf("appleContainerCombinedOutputLimited error = %v", err)
	}
	if !truncated {
		t.Fatalf("truncated = false, want true; len(out) = %d", len(out))
	}
	if len(out) != 1024 {
		t.Fatalf("len(out) = %d, want 1024", len(out))
	}
}

func TestAppleContainerCheckRequirementsRejectsUnsupportedHost(t *testing.T) {
	oldGOOS := appleContainerHostGOOS
	oldGOARCH := appleContainerHostGOARCH
	t.Cleanup(func() {
		appleContainerHostGOOS = oldGOOS
		appleContainerHostGOARCH = oldGOARCH
	})
	appleContainerHostGOOS = func() string { return "linux" }
	appleContainerHostGOARCH = func() string { return "arm64" }

	err := (&AppleContainerProvider{}).CheckRequirements(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Apple silicon Mac") {
		t.Fatalf("CheckRequirements error = %v, want Apple silicon Mac diagnostic", err)
	}
}

func stubAppleContainerHost(t *testing.T) func() {
	t.Helper()
	oldGOOS := appleContainerHostGOOS
	oldGOARCH := appleContainerHostGOARCH
	appleContainerHostGOOS = func() string { return "darwin" }
	appleContainerHostGOARCH = func() string { return "arm64" }
	return func() {
		appleContainerHostGOOS = oldGOOS
		appleContainerHostGOARCH = oldGOARCH
	}
}

func fakeAppleContainerCommandContext(logFile string) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmdArgs := []string{"-test.run=TestAppleContainerHelperProcess", "--", name}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_APPLE_CONTAINER_HELPER_PROCESS=1",
			"APPLE_CONTAINER_HELPER_LOG="+logFile,
		)
		return cmd
	}
}

func TestAppleContainerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_APPLE_CONTAINER_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if logFile := os.Getenv("APPLE_CONTAINER_HELPER_LOG"); logFile != "" {
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
	// `container <service> status`: report a service as down when the matching
	// env var is set, otherwise report it running.
	if len(args) >= 3 && args[0] == "container" && args[2] == "status" {
		if os.Getenv("APPLE_CONTAINER_HELPER_"+strings.ToUpper(args[1])+"_DOWN") != "" {
			_, _ = os.Stderr.WriteString(args[1] + " service is not running\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	// `container <service> start`.
	if len(args) >= 3 && args[0] == "container" && args[2] == "start" {
		if os.Getenv("APPLE_CONTAINER_HELPER_START_ERROR") != "" {
			_, _ = os.Stderr.WriteString("start failed\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	// `brew install <formula>`: arg0 is the brew binary path, not "container".
	if len(args) >= 2 && args[0] != "container" && args[1] == "install" {
		if os.Getenv("APPLE_CONTAINER_HELPER_BREW_ERROR") != "" {
			_, _ = os.Stderr.WriteString("brew install failed\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 2 && args[0] == "container" && args[1] == "build" {
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "stop" {
		if msg := os.Getenv("APPLE_CONTAINER_HELPER_STOP_ERROR"); msg != "" {
			_, _ = os.Stderr.WriteString(msg + "\n")
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "inspect" {
		switch os.Getenv("APPLE_CONTAINER_HELPER_INSPECT") {
		case "missing":
			_, _ = os.Stderr.WriteString("container not found\n")
			os.Exit(1)
		case "error":
			_, _ = os.Stderr.WriteString("permission denied\n")
			os.Exit(1)
		case "managed":
			_, _ = os.Stdout.WriteString(`{"configuration":{"labels":{"wendy.managed":"true"}}}`)
			os.Exit(0)
		case "large":
			_, _ = os.Stdout.WriteString(strings.Repeat("x", appleContainerMaxJSONOutputBytes))
			_, _ = os.Stdout.WriteString(strings.Repeat("x", appleContainerMaxJSONOutputBytes))
			os.Exit(0)
		}
	}
	if len(args) >= 5 && args[0] == "container" && args[1] == "list" {
		switch os.Getenv("APPLE_CONTAINER_HELPER_LIST") {
		case "one":
			_, _ = os.Stdout.WriteString(`[{"id":"myapp","image":"myapp:latest","state":"running"}]`)
			os.Exit(0)
		}
	}
	if len(args) >= 3 && args[0] == "container" && args[1] == "delete" {
		os.Exit(0)
	}
	os.Exit(1)
}
