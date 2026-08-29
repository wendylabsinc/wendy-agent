package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/appconfig"
)

const swiftStagefileSource = `version: 1
stages:
  - name: app
    from: swift:6.0
    pin: false
    build:
      lang: swift
    entrypoint:
      exec: [/app]
`

func writeSwiftStagefileProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stagefileSourceName), []byte(swiftStagefileSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFrameworkStagefileOptionsInjectROS2Middleware(t *testing.T) {
	dir := t.TempDir()
	source := `version: 1
stages:
  - name: app
    from: ros:humble-ros-base
    pin: false
`
	if err := os.WriteFile(filepath.Join(dir, stagefileSourceName), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	frameworks := &appconfig.FrameworksConfig{ROS2: &appconfig.ROS2Config{
		Distro: "humble",
		RMW:    "cyclonedds",
	}}
	if _, err := compileStagefile(dir, stagefileSourceName, "", frameworkStagefileOptions(frameworks)...); err != nil {
		t.Fatal(err)
	}
	got := generatedDockerfileText(t, dir)
	if !strings.Contains(got, "'ros-humble-rmw-cyclonedds-cpp'") {
		t.Fatalf("generated Dockerfile missing framework middleware:\n%s", got)
	}
}

func generatedDockerfileText(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, generatedDockerfileName))
	if err != nil {
		t.Fatalf("reading generated Dockerfile: %v", err)
	}
	return string(b)
}

// --debug has to reach Stagefile Swift builds, not just the native and
// swift-container-plugin paths that already honored it.
func TestCompileStagefileDebugBuildsSwiftUnoptimized(t *testing.T) {
	dir := writeSwiftStagefileProject(t)
	if _, err := compileStagefile(dir, stagefileSourceName, "", debugStagefileOptions(true)...); err != nil {
		t.Fatalf("compileStagefile: %v", err)
	}
	if got := generatedDockerfileText(t, dir); !strings.Contains(got, "-c debug") {
		t.Fatalf("want a debug build:\n%s", got)
	}
}

func TestCompileStagefileDefaultsToReleaseWithoutDebug(t *testing.T) {
	dir := writeSwiftStagefileProject(t)
	if _, err := compileStagefile(dir, stagefileSourceName, "", debugStagefileOptions(false)...); err != nil {
		t.Fatalf("compileStagefile: %v", err)
	}
	if got := generatedDockerfileText(t, dir); !strings.Contains(got, "-c release") {
		t.Fatalf("want a release build:\n%s", got)
	}
}

func TestDebugStagefileOptionsIsEmptyWhenNotDebugging(t *testing.T) {
	if got := debugStagefileOptions(false); len(got) != 0 {
		t.Fatalf("want no options, got %d", len(got))
	}
	if got := debugStagefileOptions(true); len(got) != 1 {
		t.Fatalf("want one option, got %d", len(got))
	}
}

// resolveDockerfile is the path `wendy run`/`wendy build` actually take.
func TestResolveDockerfileForwardsDebugProfileToStagefile(t *testing.T) {
	dir := writeSwiftStagefileProject(t)
	name, err := resolveDockerfile(dir, "", false, "", debugStagefileOptions(true)...)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if name != generatedDockerfileName {
		t.Fatalf("resolved %q, want %q", name, generatedDockerfileName)
	}
	if got := generatedDockerfileText(t, dir); !strings.Contains(got, "-c debug") {
		t.Fatalf("want a debug build:\n%s", got)
	}
}

// An explicitly named Stagefile takes a different branch of resolveDockerfile
// than the detected one above — it skips prepareDockerBuildFile and calls the
// compiler directly. That branch has to forward the build profile too, or
// `--dockerfile prod.stagefile.yaml --debug` silently builds release.
func TestResolveDockerfileForwardsDebugProfileToNamedStagefileVariant(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "prod.stagefile.yaml"), []byte(swiftStagefileSource), 0o644); err != nil {
		t.Fatal(err)
	}
	name, err := resolveDockerfile(dir, "prod.stagefile.yaml", false, "", debugStagefileOptions(true)...)
	if err != nil {
		t.Fatalf("resolveDockerfile: %v", err)
	}
	if want := generatedDockerfileName + ".prod"; name != want {
		t.Fatalf("resolved %q, want %q", name, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("reading generated Dockerfile: %v", err)
	}
	// Matched on the profile flag alone, not on "swift build -c debug": the
	// Swift stage interleaves --scratch-path/--cache-path between the two, so
	// pinning the whole phrase would fail on a cache-flag change rather than on
	// the profile this test is about. Asserting release is absent keeps it tight.
	got := string(b)
	if !strings.Contains(got, "-c debug") || strings.Contains(got, "-c release") {
		t.Fatalf("want a debug build:\n%s", got)
	}
}

func TestBuildCmdHasDebugFlag(t *testing.T) {
	f := newBuildCmd().Flags().Lookup("debug")
	if f == nil {
		t.Fatal("wendy build should accept --debug")
	}
	if f.DefValue != "false" {
		t.Fatalf("--debug default = %q, want false (release)", f.DefValue)
	}
}

func TestBuildxProgressModeHonorsEnvOverride(t *testing.T) {
	for env, want := range map[string]string{"plain": "plain", "rawjson": "rawjson", "RawJSON": "rawjson"} {
		t.Setenv("WENDY_BUILD_PROGRESS", env)
		if got := detectBuildxProgressMode(context.Background()); got != want {
			t.Errorf("WENDY_BUILD_PROGRESS=%q -> %q, want %q", env, got, want)
		}
	}
}

// An unparseable or missing buildx must degrade to plain rather than fail the
// build with an unsupported --progress value.
func TestBuildxProgressModeFallsBackToPlain(t *testing.T) {
	t.Setenv("WENDY_BUILD_PROGRESS", "")
	t.Setenv("PATH", t.TempDir())
	if got := detectBuildxProgressMode(context.Background()); got != "plain" {
		t.Fatalf("got %q, want plain when docker is unavailable", got)
	}
}
