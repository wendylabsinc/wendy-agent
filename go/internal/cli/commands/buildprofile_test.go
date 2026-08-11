package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if _, err := compileStagefile(dir, "", debugStagefileOptions(true)...); err != nil {
		t.Fatalf("compileStagefile: %v", err)
	}
	if got := generatedDockerfileText(t, dir); !strings.Contains(got, "-c debug") {
		t.Fatalf("want a debug build:\n%s", got)
	}
}

func TestCompileStagefileDefaultsToReleaseWithoutDebug(t *testing.T) {
	dir := writeSwiftStagefileProject(t)
	if _, err := compileStagefile(dir, "", debugStagefileOptions(false)...); err != nil {
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
